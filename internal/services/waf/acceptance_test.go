package waf_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	framework "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

const lbr = "lbr_000000000abc"

// fixture is waf-api and lb-api over memory. A WAF is stored with waf-api's defaults filled in, a
// replace needs the current version's If-Match, and a WAF attached to the load balancer can't be
// deleted.
type fixture struct {
	mu    sync.Mutex
	wafs  map[string]map[string]any // by slug
	lbWaf any                       // the load balancer's wafId: nil or a waf_ slug
	next  int
	calls []string // METHOD path If-Match=… of every WAF and load balancer write
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{wafs: map[string]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func orDefault(obj map[string]any, k string, v any) {
	if obj[k] == nil {
		obj[k] = v
	}
}

// filled is a WafInput with waf-api's defaults, as ParseSpec makes it.
func filled(in map[string]any) map[string]any {
	orDefault(in, "mode", "block")
	orDefault(in, "defaultAction", "allow")
	orDefault(in, "tags", map[string]any{})
	orDefault(in, "managedRuleSets", []any{})
	orDefault(in, "rules", []any{})
	orDefault(in, "logging", map[string]any{"scope": "off", "environment": nil})
	if _, ok := in["description"]; !ok {
		in["description"] = nil
	}
	for _, s := range in["managedRuleSets"].([]any) {
		set := s.(map[string]any)
		orDefault(set, "mode", "block")
		orDefault(set, "ruleOverrides", []any{})
		if set["name"] == "ezgh/core" {
			orDefault(set, "paranoiaLevel", 1)
		}
	}
	for _, r := range in["rules"].([]any) {
		rule := r.(map[string]any)
		if rule["action"] == "rateLimit" {
			orDefault(rule, "mode", "block")
		}
		for _, k := range []string{"mode", "rateLimit"} {
			if _, ok := rule[k]; !ok {
				rule[k] = nil
			}
		}
		for _, c := range rule["conditions"].([]any) {
			cond := c.(map[string]any)
			orDefault(cond, "negate", false)
			if t := cond["type"]; t == "regex" || t == "contains" || t == "equals" {
				orDefault(cond, "caseSensitive", false)
			}
		}
	}
	return in
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	fail := func(status int, code string) {
		send(status, map[string]any{"error": map[string]any{"code": code, "message": code}})
	}
	sendWAF := func(status int, obj map[string]any) {
		obj["attachments"] = []any{}
		if f.lbWaf == obj["slug"] {
			obj["attachments"] = []any{lbr}
		}
		w.Header().Set("ETag", strconv.Quote(fmt.Sprint(obj["version"])))
		send(status, obj)
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Method != "GET" {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+" If-Match="+r.Header.Get("If-Match"))
		if (r.Method == "POST" || r.Method == "PATCH") && r.Header.Get("Idempotency-Key") == "" {
			fail(400, "idempotency_key_required")
			return
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/v1/organizations/org_test/managedRuleSets":
		var sets []any
		for _, n := range []string{"ezgh/core", "ezgh/php", "ezgh/node", "ezgh/known-bad-inputs"} {
			sets = append(sets, map[string]any{"name": n, "description": n + " rules", "version": "2026.11.1", "updatedAt": "2026-11-01T00:00:00.000Z"})
		}
		send(200, map[string]any{"managedRuleSets": sets, "nextCursor": nil})
	case len(parts) == 5 && parts[3] == "projects":
		send(200, map[string]any{"id": "prj_test"})
	case len(parts) == 7 && parts[5] == "load-balancers":
		if parts[6] != lbr {
			fail(404, "not_found")
			return
		}
		if r.Method == "PATCH" {
			if id, ok := body["wafId"].(string); ok && f.wafs[id] == nil {
				fail(400, "invalid_request")
				return
			}
			f.lbWaf = body["wafId"]
		}
		send(200, map[string]any{"slug": lbr, "wafId": f.lbWaf, "waf": nil})
	case len(parts) == 6 && parts[5] == "wafs" && r.Method == "POST":
		f.next++
		slug := fmt.Sprintf("waf_%012d", f.next)
		obj := filled(body)
		for k, v := range map[string]any{"id": "0193-uuid", "slug": slug, "resourceName": "ezgh:waf:us-west-1:org_test:prj_test/" + slug, "status": "active", "version": 1} {
			obj[k] = v
		}
		f.wafs[slug] = obj
		sendWAF(201, obj)
	case len(parts) == 7 && parts[5] == "wafs":
		obj := f.wafs[parts[6]]
		if obj == nil {
			fail(404, "not_found")
			return
		}
		switch r.Method {
		case "GET":
			sendWAF(200, obj)
		case "PUT":
			if r.Header.Get("If-Match") != strconv.Quote(fmt.Sprint(obj["version"])) {
				fail(412, "precondition_failed")
				return
			}
			next := filled(body)
			for _, k := range []string{"id", "slug", "resourceName", "status"} {
				next[k] = obj[k]
			}
			next["version"] = obj["version"].(int) + 1
			f.wafs[parts[6]] = next
			sendWAF(200, next)
		case "DELETE":
			if f.lbWaf == parts[6] {
				fail(409, "waf_in_use")
				return
			}
			delete(f.wafs, parts[6])
			w.WriteHeader(204)
		}
	default:
		fail(404, "not_found")
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, waf = %[1]q, lb = %[1]q }
}
`, f.URL)
}

// called checks some write was made: a METHOD path If-Match=… line of f.calls.
func (f *fixture) called(call string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.calls {
			if c == call {
				return nil
			}
		}
		return fmt.Errorf("no %q in %q", call, f.calls)
	}
}

func gone(f *fixture) func(*terraform.State) error {
	return func(*terraform.State) error {
		if len(f.wafs) != 0 || f.lbWaf != nil {
			return fmt.Errorf("left %v, load balancer WAF %v", f.wafs, f.lbWaf)
		}
		return nil
	}
}

func action(name string, a plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, a)}}
}

// wafConfig is a WAF using every kind of nested value; second is the second rule's body.
func wafConfig(mode, second string) string {
	return fmt.Sprintf(`
resource "ezghcloud_waf" "web" {
 project_id = "prj_test"
 name = "web"
 mode = %q
 tags = { team = "web" }
 managed_rule_sets = [
  { name = "ezgh/core" },
  { name = "ezgh/php", mode = "log", rule_overrides = [{ rule_id = 933100, action = "log" }] },
 ]
 rules = [
  { name = "office", action = "allow", conditions = [{ type = "ipSet", cidrs = ["203.0.113.0/24", "2001:db8::/32"] }] },
  %s,
  { name = "no-bad-asn", action = "block", conditions = [{ type = "asn", numbers = [64496] }, { type = "country", codes = ["US"], negate = true }] },
 ]
 logging = { scope = "matched", environment = "production" }
}
`, mode, second)
}

const loginLimit = `{ name = "login", action = "rateLimit", rate_limit = { limit = 100, window_seconds = 60 }, conditions = [{ type = "equals", target = "path", value = "/login" }] }`

func TestAccWAFLifecycle(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_WAF", "1")
	f := newFixture(t)
	r := "ezghcloud_waf.web"
	data := `
data "ezghcloud_waf_managed_rule_sets" "all" {}
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: gone(f), Steps: []resource.TestStep{
		{Config: f.provider() + wafConfig("block", loginLimit) + data, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "waf_000000000001"),
			resource.TestCheckResourceAttr(r, "version", "1"),
			resource.TestCheckResourceAttr(r, "default_action", "allow"),
			resource.TestCheckResourceAttr(r, "attachments.#", "0"),
			resource.TestCheckResourceAttr(r, "managed_rule_sets.0.mode", "block"),
			resource.TestCheckResourceAttr(r, "managed_rule_sets.0.paranoia_level", "1"),
			resource.TestCheckResourceAttr(r, "managed_rule_sets.0.rule_overrides.#", "0"),
			resource.TestCheckNoResourceAttr(r, "managed_rule_sets.1.paranoia_level"),
			resource.TestCheckResourceAttr(r, "managed_rule_sets.1.rule_overrides.0.rule_id", "933100"),
			resource.TestCheckNoResourceAttr(r, "rules.0.mode"),
			resource.TestCheckNoResourceAttr(r, "rules.0.conditions.0.case_sensitive"),
			resource.TestCheckResourceAttr(r, "rules.0.conditions.0.negate", "false"),
			resource.TestCheckResourceAttr(r, "rules.1.mode", "block"),
			resource.TestCheckResourceAttr(r, "rules.1.rate_limit.window_seconds", "60"),
			resource.TestCheckResourceAttr(r, "rules.1.conditions.0.case_sensitive", "false"),
			resource.TestCheckResourceAttr(r, "rules.2.conditions.1.negate", "true"),
			resource.TestCheckResourceAttr(r, "logging.environment", "production"),
			resource.TestCheckResourceAttr("data.ezghcloud_waf_managed_rule_sets.all", "managed_rule_sets.#", "4"),
			resource.TestCheckResourceAttr("data.ezghcloud_waf_managed_rule_sets.all", "managed_rule_sets.0.name", "ezgh/core"))},
		// A replace sends the version read; the rate limit turns into a case-sensitive regex block.
		{Config: f.provider() + wafConfig("log", `{ name = "login", action = "block", conditions = [{ type = "regex", target = "header:User-Agent", value = "^curl/", case_sensitive = true }] }`),
			ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate), Check: resource.ComposeAggregateTestCheckFunc(
				f.called("PUT /v1/organizations/org_test/projects/prj_test/wafs/waf_000000000001 If-Match=\"1\""),
				resource.TestCheckResourceAttr(r, "id", "waf_000000000001"),
				resource.TestCheckResourceAttr(r, "version", "2"),
				resource.TestCheckResourceAttr(r, "mode", "log"),
				resource.TestCheckNoResourceAttr(r, "rules.1.mode"),
				resource.TestCheckNoResourceAttr(r, "rules.1.rate_limit"),
				resource.TestCheckResourceAttr(r, "rules.1.conditions.0.case_sensitive", "true"))},
		// Dropping the nested values returns them to their defaults.
		{Config: f.provider() + `
resource "ezghcloud_waf" "web" {
 project_id = "prj_test"
 name = "web"
}
`, ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate), Check: resource.ComposeAggregateTestCheckFunc(
			f.called("PUT /v1/organizations/org_test/projects/prj_test/wafs/waf_000000000001 If-Match=\"2\""),
			resource.TestCheckResourceAttr(r, "mode", "block"),
			resource.TestCheckResourceAttr(r, "rules.#", "0"),
			resource.TestCheckResourceAttr(r, "managed_rule_sets.#", "0"),
			resource.TestCheckResourceAttr(r, "logging.scope", "off"),
			resource.TestCheckResourceAttr(r, "tags.%", "0"))},
		{Config: f.provider() + wafConfig("block", loginLimit), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate)},
		{ResourceName: r, ImportState: true, ImportStateId: "org_test/prj_test/waf_000000000001", ImportStateVerify: true},
		{ResourceName: r, ImportState: true, ImportStateId: "waf_000000000001", ExpectError: regexp.MustCompile(`organization_id/project_id/waf_`)},
		// Changed outside Terraform: refresh shows the drift, and the replace reverts it.
		{PreConfig: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			w := f.wafs["waf_000000000001"]
			w["mode"], w["version"] = "log", w["version"].(int)+1
		}, Config: f.provider() + wafConfig("block", loginLimit), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate),
			Check: resource.TestCheckResourceAttr(r, "mode", "block")},
	}})
}

func TestAccLoadBalancerWAF(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_WAF", "1")
	f := newFixture(t)
	r := "ezghcloud_load_balancer_waf.web"
	config := func(waf string) string {
		return f.provider() + wafConfig("block", loginLimit) + fmt.Sprintf(`
resource "ezghcloud_waf" "other" {
 project_id = "prj_test"
 name = "other"
}
resource "ezghcloud_load_balancer_waf" "web" {
 project_id = "prj_test"
 load_balancer_id = %q
 waf_id = ezghcloud_waf.%s.id
}
`, lbr, waf)
	}
	// attached checks the load balancer has the WAF that's ezghcloud_waf.<name> in state.
	attached := func(name string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id := s.RootModule().Resources["ezghcloud_waf."+name].Primary.ID; f.lbWaf != id {
				return fmt.Errorf("load balancer has WAF %v, not %s", f.lbWaf, id)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: gone(f), Steps: []resource.TestStep{
		{Config: config("web"), Check: resource.ComposeAggregateTestCheckFunc(
			attached("web"),
			resource.TestCheckResourceAttr(r, "id", lbr),
			resource.TestCheckResourceAttrPair(r, "waf_id", "ezghcloud_waf.web", "id"))},
		{Config: config("other"), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate), Check: attached("other")},
		{ResourceName: r, ImportState: true, ImportStateId: "org_test/prj_test/" + lbr, ImportStateVerify: true},
		// Detached outside Terraform: the next apply attaches it again.
		{PreConfig: func() { f.mu.Lock(); f.lbWaf = nil; f.mu.Unlock() }, Config: config("other"), ConfigPlanChecks: action(r, plancheck.ResourceActionCreate),
			Check: attached("other")},
		// Left attached (the attachment dropped from state only), the WAF can't be deleted.
		{Config: f.provider() + wafConfig("block", loginLimit) + `
removed {
 from = ezghcloud_load_balancer_waf.web
 lifecycle { destroy = false }
}
`, ExpectError: regexp.MustCompile(`waf_in_use`)},
		{PreConfig: func() { f.mu.Lock(); f.lbWaf = nil; f.mu.Unlock() }, Config: f.provider()},
	}})
}

// Without EZGHCLOUD_PREVIEW_WAF=1 the provider serves no WAF resource or data source, its endpoints
// description names neither waf nor lb, and an endpoints.waf or endpoints.lb override is refused.
func TestWAFHiddenWithoutPreview(t *testing.T) {
	ctx := context.Background()
	for _, preview := range []string{"", "1"} {
		t.Setenv("EZGHCLOUD_PREVIEW_WAF", preview)
		p := provider.New("test")()
		var names []string
		for _, r := range p.Resources(ctx) {
			var resp fwresource.MetadataResponse
			r().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "ezghcloud"}, &resp)
			names = append(names, resp.TypeName)
		}
		for _, d := range p.DataSources(ctx) {
			var resp datasource.MetadataResponse
			d().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "ezghcloud"}, &resp)
			names = append(names, resp.TypeName)
		}
		var sr framework.SchemaResponse
		p.Schema(ctx, framework.SchemaRequest{}, &sr)
		description := sr.Schema.Attributes["endpoints"].GetDescription()
		described := strings.Contains(description, "waf") || strings.Contains(description, " lb,")
		served := strings.Contains(strings.Join(names, " "), "waf")
		if served != (preview == "1") || described != (preview == "1") {
			t.Fatalf("preview %q: served %v, described %v: %v", preview, served, described, names)
		}
		for _, key := range []string{"waf", "lb"} {
			raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
				"api_token":       tftypes.NewValue(tftypes.String, "token"),
				"organization_id": tftypes.NewValue(tftypes.String, "org_test"),
				"endpoints":       tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{key: tftypes.NewValue(tftypes.String, "https://"+key+".test")}),
			})
			var resp framework.ConfigureResponse
			p.Configure(ctx, framework.ConfigureRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}, &resp)
			if resp.Diagnostics.HasError() != (preview == "") {
				t.Fatalf("preview %q, endpoints.%s: %v", preview, key, resp.Diagnostics)
			}
		}
	}
}
