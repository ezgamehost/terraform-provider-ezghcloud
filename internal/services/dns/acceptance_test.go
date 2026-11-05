package dns_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// fixture is dns-api over memory: one zone at a time and its record sets.
type fixture struct {
	mu      sync.Mutex
	zone    map[string]any
	sets    []map[string]any
	calls   []string         // METHOD path of every dns call
	changes []map[string]any // each records/changes body
	// refuseUpsert answers every upsert 409 managed_record_set.
	refuseUpsert bool
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

const zoneBase = "/v1/organizations/org_test/projects/prj_test/zones"

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := r.URL.Path
	switch {
	case p == "/v1/organizations/org_test/projects/prj_test":
		send(200, map[string]any{"slug": "prj_test"})
		return
	case p == "/v1/organizations/org_test/projects":
		send(200, map[string]any{"items": []any{map[string]any{"slug": "prj_other"}, map[string]any{"slug": "prj_test"}}, "nextCursor": nil})
		return
	}
	if !strings.HasPrefix(p, zoneBase) {
		send(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
		return
	}
	f.calls = append(f.calls, r.Method+" "+strings.TrimPrefix(p, zoneBase))
	rest := strings.TrimPrefix(p, zoneBase)
	switch {
	case rest == "" && r.Method == "POST":
		f.zone = map[string]any{"slug": "zon_000000000001", "domain": body["domain"], "tags": body["tags"], "status": "pending",
			"nameservers": []any{"ns1.ezgh.example"}, "verification": map[string]any{"txtName": "_ezgh-verify.example.com", "txtValue": "v1", "lastCheckedAt": nil},
			"dnssec": map[string]any{"enabled": false, "dsRecords": []any{}}}
		send(201, f.zone)
	case rest == "" && r.Method == "GET":
		send(200, map[string]any{"zones": []any{f.zone}, "nextCursor": nil})
	case f.zone == nil || !strings.HasPrefix(rest, "/zon_000000000001"):
		send(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
	case rest == "/zon_000000000001" && r.Method == "GET":
		send(200, f.zone)
	case rest == "/zon_000000000001" && r.Method == "DELETE":
		f.zone = nil
		w.WriteHeader(204)
	case strings.HasSuffix(rest, "/dnssec/enable"), strings.HasSuffix(rest, "/dnssec/disable"):
		f.zone["dnssec"] = map[string]any{"enabled": strings.HasSuffix(rest, "enable"), "dsRecords": []any{}}
		send(200, f.zone)
	case strings.HasSuffix(rest, "/records") && r.Method == "GET":
		send(200, map[string]any{"recordSets": append([]map[string]any{}, f.sets...), "nextCursor": nil})
	case strings.HasSuffix(rest, "/records/changes"):
		f.changes = append(f.changes, body)
		for _, c := range body["changes"].([]any) {
			ch := c.(map[string]any)
			f.sets = slices.DeleteFunc(f.sets, func(s map[string]any) bool { return s["name"] == ch["name"] && s["type"] == ch["type"] })
			if ch["action"] == "upsert" {
				if f.refuseUpsert {
					send(409, map[string]any{"error": map[string]any{"code": "managed_record_set", "message": "Managed by certificates"}})
					return
				}
				f.sets = append(f.sets, map[string]any{"name": ch["name"], "type": ch["type"], "ttl": ch["ttl"], "values": ch["values"], "managedBy": nil})
			}
		}
		send(200, f.zone)
	default:
		send(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, dns = %[1]q }
}
`, f.URL)
}

func (f *fixture) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

const zone = `
resource "ezghcloud_dns_zone" "main" {
 project_id = "prj_test"
 domain = "example.com"
 dnssec_enabled = %t
}
`

func TestAccZoneAndDataSource(t *testing.T) {
	f := newFixture(t)
	r := "ezghcloud_dns_zone.main"
	data := `
data "ezghcloud_dns_zone" "by_domain" {
 project_id = "prj_test"
 domain = ezghcloud_dns_zone.main.domain
}
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(zone, true) + data, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "zon_000000000001"),
			resource.TestCheckResourceAttr(r, "dnssec_enabled", "true"),
			resource.TestCheckResourceAttr(r, "nameservers.0", "ns1.ezgh.example"),
			resource.TestCheckResourceAttr(r, "verification.txt_name", "_ezgh-verify.example.com"),
			resource.TestCheckResourceAttr("data.ezghcloud_dns_zone.by_domain", "id", "zon_000000000001"),
			func(*terraform.State) error {
				if !f.called("POST /zon_000000000001/dnssec/enable") {
					return fmt.Errorf("dnssec/enable was not called: %v", f.calls)
				}
				return nil
			})},
		{Config: f.provider() + fmt.Sprintf(zone, false), Check: resource.TestCheckResourceAttr(r, "dnssec_enabled", "false")},
		{ResourceName: r, ImportState: true, ImportStateId: "prj_test/zon_000000000001", ImportStateVerify: true},
	}})
	if f.zone != nil {
		t.Fatal("zone left behind")
	}
}

const recordSet = `
resource "ezghcloud_dns_record_set" "www" {
 project_id = "prj_test"
 zone_id = "zon_000000000001"
 name = "www"
 type = "A"
 ttl = %d
 values = ["192.0.2.1"]
}
`

func TestAccRecordSetLifecycle(t *testing.T) {
	f := newFixture(t)
	f.zone = map[string]any{"slug": "zon_000000000001", "domain": "example.com", "tags": map[string]any{}, "status": "active", "nameservers": []any{},
		"verification": map[string]any{"txtName": "", "txtValue": ""}, "dnssec": map[string]any{"enabled": false}}
	r := "ezghcloud_dns_record_set.www"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(recordSet, 300), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "zon_000000000001/www/A"),
			resource.TestCheckResourceAttr(r, "ttl", "300"),
			resource.TestCheckResourceAttr(r, "managed_by.%", "0"),
		)},
		{Config: f.provider() + fmt.Sprintf(recordSet, 60), Check: resource.TestCheckResourceAttr(r, "ttl", "60")},
		{ResourceName: r, ImportState: true, ImportStateId: "zon_000000000001/www/A", ImportStateVerify: true},
		{ResourceName: r, ImportState: true, ImportStateId: "prj_test/zon_000000000001/www/A", ImportStateVerify: true},
	}})
	want := `[{"changes":[{"action":"upsert","name":"www","ttl":300,"type":"A","values":["192.0.2.1"]}]},` +
		`{"changes":[{"action":"upsert","name":"www","ttl":60,"type":"A","values":["192.0.2.1"]}]},` +
		`{"changes":[{"action":"delete","name":"www","type":"A"}]}]`
	got, _ := json.Marshal(f.changes)
	if string(got) != want {
		t.Fatalf("changes:\n got %s\nwant %s", got, want)
	}
}

func TestAccRecordSetManagedRefused(t *testing.T) {
	f := newFixture(t)
	f.zone = map[string]any{"slug": "zon_000000000001", "domain": "example.com", "tags": map[string]any{}, "status": "active", "nameservers": []any{},
		"verification": map[string]any{"txtName": "", "txtValue": ""}, "dnssec": map[string]any{"enabled": false}}
	f.refuseUpsert = true
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(recordSet, 300), ExpectError: regexp.MustCompile(`(?s)managed by another EZGH`)},
	}})
}

func TestAccRecordSetExistsRefused(t *testing.T) {
	f := newFixture(t)
	f.zone = map[string]any{"slug": "zon_000000000001", "domain": "example.com", "tags": map[string]any{}, "status": "active", "nameservers": []any{},
		"verification": map[string]any{"txtName": "", "txtValue": ""}, "dnssec": map[string]any{"enabled": false}}
	f.sets = []map[string]any{{"name": "www", "type": "A", "ttl": 300, "values": []any{"192.0.2.9"}, "managedBy": nil}}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(recordSet, 300), ExpectError: regexp.MustCompile(`already exists`)},
	}})
}

func (f *fixture) activeZone() {
	f.zone = map[string]any{"slug": "zon_000000000001", "domain": "example.com", "tags": map[string]any{}, "status": "active", "nameservers": []any{},
		"verification": map[string]any{"txtName": "", "txtValue": ""}, "dnssec": map[string]any{"enabled": false}}
}

// A record set another service owns can't be created or imported: no apply could ever clear it.
func TestAccManagedRecordSetRefusedOnCreateAndImport(t *testing.T) {
	f := newFixture(t)
	f.activeZone()
	f.sets = []map[string]any{{"name": "www", "type": "A", "ttl": 300, "values": []any{"192.0.2.9"},
		"managedBy": map[string]any{"service": "certificates", "resourceName": "ezgh:certificates::org_test:dau_000000000001"}}}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(recordSet, 300), ExpectError: regexp.MustCompile(`managed by Web Certificates`)},
		{Config: f.provider() + fmt.Sprintf(recordSet, 300), ResourceName: "ezghcloud_dns_record_set.www", ImportState: true, ImportStateId: "zon_000000000001/www/A",
			ImportStatePersist: false, ExpectError: regexp.MustCompile(`managed by Web Certificates`)},
	}})
	if len(f.changes) != 0 {
		t.Fatalf("a managed set was written: %v", f.changes)
	}
}

func TestAccZoneTagChangeRefused(t *testing.T) {
	f := newFixture(t)
	withTags := func(v string) string {
		return f.provider() + fmt.Sprintf(`
resource "ezghcloud_dns_zone" "main" {
 project_id = "prj_test"
 domain = "example.com"
 tags = { team = %q }
}
`, v)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: withTags("a")},
		{Config: withTags("b"), ExpectError: regexp.MustCompile(`tags can't change`)},
	}})
}

func TestAccNonCanonicalNamesRefused(t *testing.T) {
	f := newFixture(t)
	f.activeZone()
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + strings.Replace(fmt.Sprintf(recordSet, 300), `name = "www"`, `name = "WWW"`, 1), ExpectError: regexp.MustCompile(`canonical form`)},
		{Config: f.provider() + strings.Replace(fmt.Sprintf(zone, false), `example.com`, `Example.com.`, 1), ExpectError: regexp.MustCompile(`canonical form`)},
	}})
	if len(f.changes) != 0 || len(f.calls) != 0 {
		t.Fatalf("the API was called: %v", f.calls)
	}
}

// TestLiveDNS creates a zone and a record set on real EZGH. It runs only with TF_ACC=1 and
// EZGH_LIVE=1: the explicit opt-in, never in CI. Use a test organization.
func TestLiveDNS(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_LIVE") != "1" {
		t.Skip("set TF_ACC=1 and EZGH_LIVE=1 to run against real EZGH")
	}
	project := os.Getenv("EZGHCLOUD_TEST_PROJECT_ID")
	if project == "" {
		t.Fatal("EZGHCLOUD_TEST_PROJECT_ID is required")
	}
	config := fmt.Sprintf(`
resource "ezghcloud_dns_zone" "live" {
 project_id = %[1]q
 domain = "tf-live-dns.example.com"
}
resource "ezghcloud_dns_record_set" "live" {
 project_id = %[1]q
 zone_id = ezghcloud_dns_zone.live.id
 name = "www"
 type = "A"
 values = ["192.0.2.1"]
}
`, project)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{{Config: config}}})
}
