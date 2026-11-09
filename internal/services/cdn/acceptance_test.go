package cdn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// fixture is cdn-api over memory: a distribution is stored with cdn-api's defaults filled in, and a
// replace needs the ETag of the current version as If-Match.
type fixture struct {
	mu       sync.Mutex
	items    map[string]map[string]any // by slug
	versions map[string]int
	next     int
	calls    []string // METHOD If-Match=… of every write
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{items: map[string]map[string]any{}, versions: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if strings.Contains(r.URL.Path, "/projects/") && !strings.Contains(r.URL.Path, "/distributions") {
		reply(200, map[string]any{"id": "prj"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	slug := ""
	if parts[len(parts)-1] != "distributions" {
		slug = parts[len(parts)-1]
	}
	if r.Method != "GET" {
		f.calls = append(f.calls, r.Method+" If-Match="+r.Header.Get("If-Match"))
	}
	spec := func() (map[string]any, bool) {
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			reply(400, map[string]any{"error": map[string]any{"code": "invalid_request", "message": "bad body"}})
			return nil, false
		}
		return in, true
	}
	etag := func(s string) { w.Header().Set("ETag", strconv.Quote(strconv.Itoa(f.versions[s]))) }
	switch {
	case r.Method == "POST":
		in, ok := spec()
		if !ok {
			return
		}
		f.next++
		s := fmt.Sprintf("cdn_%012d", f.next)
		in["id"], in["slug"], in["status"], in["defaultHostname"] = "uuid-"+s, s, "deploying", "d"+s[4:]+".ezghcdn.com"
		in["resourceName"] = "ezgh:us-west-1:org:prj:" + s
		f.items[s], f.versions[s] = in, 1
		etag(s)
		reply(201, in)
	case f.items[slug] == nil:
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Distribution not found"}})
	case r.Method == "GET":
		etag(slug)
		reply(200, f.items[slug])
	case r.Method == "PUT":
		if r.Header.Get("If-Match") != strconv.Quote(strconv.Itoa(f.versions[slug])) {
			reply(412, map[string]any{"error": map[string]any{"code": "precondition_failed", "message": "The distribution changed since it was read"}})
			return
		}
		in, ok := spec()
		if !ok {
			return
		}
		for _, k := range []string{"id", "slug", "defaultHostname", "resourceName"} {
			in[k] = f.items[slug][k]
		}
		in["status"] = "deploying"
		f.items[slug] = in
		f.versions[slug]++
		etag(slug)
		reply(200, in)
	case r.Method == "DELETE":
		delete(f.items, slug)
		w.WriteHeader(204)
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`provider "ezghcloud" {
 api_token = "token"
 organization_id = "org"
 endpoints = { cdn = %[1]q, orgs = %[1]q }
}
`, f.URL)
}

func config(f *fixture, enabled bool, domains string) string {
	return f.provider() + fmt.Sprintf(`
resource "ezghcloud_cdn_distribution" "web" {
 project_id = "prj"
 name = "web"
 enabled = %t
 origin = { host = "origin.example.com", protocol = "http" }
%s
}
data "ezghcloud_cdn_distribution" "web" {
 project_id = "prj"
 id = ezghcloud_cdn_distribution.web.id
}
`, enabled, domains)
}

func TestAccCDNDistributionLifecycle(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_CDN", "1")
	f := newFixture(t)
	domain := `domain {
 hostname = "www.example.com"
 certificate_id = "crt_2m9x0a7qpe4k"
}`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if len(f.items) != 0 {
				return fmt.Errorf("left %d distributions", len(f.items))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(f, true, ""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "id", "cdn_000000000001"),
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "default_hostname", "d000000000001.ezghcdn.com"),
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "origin.port", "80"),
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "origin.host_header", "viewer"),
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "status", "deploying"),
				resource.TestCheckResourceAttr("data.ezghcloud_cdn_distribution.web", "origin.protocol", "http"))},
			{Config: config(f, false, domain), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "enabled", "false"),
				resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "domain.0.certificate_id", "crt_2m9x0a7qpe4k"),
				func(*terraform.State) error {
					// The replace is conditional on the ETag the create read.
					if got := f.calls[len(f.calls)-1]; got != `PUT If-Match="1"` {
						return fmt.Errorf("last write %s", got)
					}
					return nil
				})},
			{ResourceName: "ezghcloud_cdn_distribution.web", ImportState: true, ImportStateId: "org/prj/cdn_000000000001", ImportStateVerify: true},
		}})
}

// Without EZGHCLOUD_PREVIEW_CDN=1 the provider serves no CDN resource or data source, its endpoints
// description doesn't name cdn, and an endpoints.cdn override is refused.
func TestCDNHiddenWithoutPreview(t *testing.T) {
	ctx := context.Background()
	for _, preview := range []string{"", "1"} {
		t.Setenv("EZGHCLOUD_PREVIEW_CDN", preview)
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
		described := strings.Contains(sr.Schema.Attributes["endpoints"].GetDescription(), "cdn")
		served := strings.Contains(strings.Join(names, " "), "cdn")
		if served != (preview == "1") || described != (preview == "1") {
			t.Fatalf("preview %q: served %v, described %v", preview, served, described)
		}
		raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
			"api_token":       tftypes.NewValue(tftypes.String, "token"),
			"organization_id": tftypes.NewValue(tftypes.String, "org_test"),
			"endpoints":       tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{"cdn": tftypes.NewValue(tftypes.String, "https://cdn.test")}),
		})
		var resp framework.ConfigureResponse
		p.Configure(ctx, framework.ConfigureRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}, &resp)
		if resp.Diagnostics.HasError() != (preview == "") {
			t.Fatalf("preview %q, endpoints.cdn: %v", preview, resp.Diagnostics)
		}
	}
}

// An origin or domain list built from other resources is unknown at plan: validation waits for it.
func TestAccCDNDistributionUnknownAtPlan(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_CDN", "1")
	f := newFixture(t)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + `
resource "terraform_data" "host" { input = "origin.example.com" }
resource "terraform_data" "domains" { input = ["www.example.com"] }
resource "ezghcloud_cdn_distribution" "web" {
 project_id = "prj"
 name = "web"
 origin = { host = terraform_data.host.output }
 dynamic "domain" {
  for_each = terraform_data.domains.output
  content {
   hostname = domain.value
   certificate_id = "crt_2m9x0a7qpe4k"
  }
 }
}
`, Check: resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.web", "domain.0.hostname", "www.example.com")},
	}})
}
