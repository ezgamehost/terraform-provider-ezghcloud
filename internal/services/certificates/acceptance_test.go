package certificates_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

const (
	authUUID = "0193aaaa-0000-7000-8000-000000000001"
	authSlug = "dau_000000000001"
	certSlug = "crt_000000000001"
)

// fixture is certificates-api over memory: one authorization and one certificate.
type fixture struct {
	mu    sync.Mutex
	auth  map[string]any
	cert  map[string]any
	posts []map[string]any // POST bodies, authorization then certificate
	patch []map[string]any // authorization PATCH bodies
	// failIssuance ends the certificate create's operation failed with this message.
	failIssuance string
	opFailed     bool
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

const org = "/v1/organizations/org_test"

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	notFound := func() {
		send(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	accepted := func(op string) {
		w.Header().Set("Location", org+"/operations/"+op)
		send(202, map[string]any{"id": op, "status": "queued", "target": map[string]any{"id": "0193", "resourceName": "ezgh:certificates:us-west-1:org_test:prj_test/" + certSlug}})
	}
	p := r.URL.Path
	switch {
	case p == org+"/projects/prj_test":
		send(200, map[string]any{"slug": "prj_test"})
	case p == org+"/domain-authorizations" && r.Method == "POST":
		f.posts = append(f.posts, body)
		f.auth = map[string]any{"id": authUUID, "slug": authSlug, "name": body["domainName"], "domainName": body["domainName"], "enabled": true, "status": "pending",
			"tags": body["tags"], "dnsInstructions": []any{map[string]any{"type": "TXT", "name": "_acme_request_ezgh.example.com", "value": "ezgh-v1=t"}},
			"dnsZoneId": nil, "managedDns": nil}
		f.link(body)
		send(201, f.auth)
	case strings.HasPrefix(p, org+"/domain-authorizations/") && f.auth != nil && (strings.HasSuffix(p, authSlug) || strings.HasSuffix(p, authUUID)):
		switch r.Method {
		case "GET":
			send(200, f.auth)
		case "PATCH":
			f.patch = append(f.patch, body)
			for _, k := range []string{"name", "enabled", "tags"} {
				f.auth[k] = body[k]
			}
			f.link(body)
			send(200, f.auth)
		case "DELETE":
			f.auth = nil
			w.WriteHeader(204)
		}
	case p == org+"/projects/prj_test/certificates" && r.Method == "POST":
		f.posts = append(f.posts, body)
		f.cert = map[string]any{"slug": certSlug, "name": body["name"], "dnsNames": body["dnsNames"], "domainAuthorizationIds": body["domainAuthorizationIds"], "keyAlgorithm": body["keyAlgorithm"],
			"renewalEnabled": body["renewalEnabled"], "tags": body["tags"], "status": "issued", "activeVersionId": "ver-1"}
		if f.failIssuance != "" {
			f.cert["status"], f.cert["activeVersionId"], f.opFailed = "failed", nil, true
		}
		accepted("op-create")
	case p == org+"/projects/prj_test/certificates/"+certSlug && f.cert != nil:
		switch r.Method {
		case "GET":
			send(200, f.cert)
		case "DELETE":
			f.cert = nil
			accepted("op-delete")
		}
	case p == org+"/operations/op-create" && f.opFailed:
		send(200, map[string]any{"id": "op-create", "status": "failed", "error": map[string]any{"code": "issuance_failed", "message": f.failIssuance}})
	case strings.HasPrefix(p, org+"/operations/"):
		send(200, map[string]any{"id": "op", "status": "succeeded"})
	default:
		notFound()
	}
}

// link applies a body's dnsZoneId (null unlinks) to the authorization.
func (f *fixture) link(body map[string]any) {
	if zone, ok := body["dnsZoneId"]; ok {
		f.auth["dnsZoneId"], f.auth["managedDns"] = zone, nil
		if zone != nil {
			f.auth["managedDns"] = map[string]any{"zoneId": zone, "status": "synced", "detail": nil, "updatedAt": "2026-11-04T00:00:00.000Z"}
		}
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, certificates = %[1]q }
}
`, f.URL)
}

const authorization = `
resource "ezghcloud_certificate_domain_authorization" "example" {
 domain_name = "example.com"
 %s
}
`

func TestAccAuthorizationLinksAndUnlinksZone(t *testing.T) {
	f := newFixture(t)
	r := "ezghcloud_certificate_domain_authorization.example"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + fmt.Sprintf(authorization, `dns_zone_id = "zon_000000000001"`), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", authSlug),
			resource.TestCheckResourceAttr(r, "dns_zone_id", "zon_000000000001"),
			resource.TestCheckResourceAttr(r, "managed_dns.status", "synced"),
			resource.TestCheckResourceAttr(r, "dns_instructions.0.type", "TXT"),
			func(*terraform.State) error {
				if f.posts[0]["dnsZoneId"] != "zon_000000000001" {
					return fmt.Errorf("create body: %v", f.posts[0])
				}
				return nil
			})},
		{Config: f.provider() + fmt.Sprintf(authorization, ``), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckNoResourceAttr(r, "dns_zone_id"),
			resource.TestCheckNoResourceAttr(r, "managed_dns.status"),
			func(*terraform.State) error {
				got, _ := json.Marshal(f.patch[len(f.patch)-1]["dnsZoneId"])
				if _, ok := f.patch[len(f.patch)-1]["dnsZoneId"]; !ok || string(got) != "null" {
					return fmt.Errorf("unlink PATCH: %v", f.patch)
				}
				return nil
			})},
		{ResourceName: r, ImportState: true, ImportStateId: authSlug, ImportStateVerify: true},
	}})
	if f.auth != nil {
		t.Fatal("authorization left behind")
	}
}

const chain = `
resource "ezghcloud_certificate_domain_authorization" "example" {
 domain_name = "example.com"
 dns_zone_id = "zon_000000000001"
}
resource "ezghcloud_certificate" "example" {
 project_id = "prj_test"
 name = "example"
 dns_names = ["example.com"]
 domain_authorization_ids = [ezghcloud_certificate_domain_authorization.example.id]
}
`

func TestAccCertificateWaitsForIssuance(t *testing.T) {
	f := newFixture(t)
	r := "ezghcloud_certificate.example"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + chain, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", certSlug),
			resource.TestCheckResourceAttr(r, "status", "issued"),
			resource.TestCheckResourceAttr(r, "active_version", "ver-1"),
			resource.TestCheckResourceAttr(r, "key_algorithm", "ecdsa-p256"),
			func(*terraform.State) error {
				// The API takes the authorization's UUID in domainAuthorizationIds.
				if ids := f.posts[1]["domainAuthorizationIds"].([]any); len(ids) != 1 || ids[0] != authUUID {
					return fmt.Errorf("certificate create body: %v", f.posts[1])
				}
				return nil
			})},
		{ResourceName: r, ImportState: true, ImportStateId: "prj_test/" + certSlug, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
	}})
	if f.cert != nil || f.auth != nil {
		t.Fatal("resources left behind")
	}
}

func TestAccCertificateFailedOperation(t *testing.T) {
	f := newFixture(t)
	f.failIssuance = "CAA forbids issuance"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + chain, ExpectError: regexp.MustCompile(`(?s)issuance_failed.*CAA forbids issuance`)},
	}})
}

func TestAccNonCanonicalNamesRefused(t *testing.T) {
	f := newFixture(t)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: f.provider() + `
resource "ezghcloud_certificate_domain_authorization" "x" {
 domain_name = "Example.com"
}
`, ExpectError: regexp.MustCompile(`canonical form`)},
		{Config: f.provider() + strings.Replace(chain, `["example.com"]`, `["*.Example.com"]`, 1), ExpectError: regexp.MustCompile(`canonical form`)},
	}})
	if len(f.posts) != 0 {
		t.Fatalf("the API was called: %v", f.posts)
	}
}

// TestLiveCertificate runs the zone, authorization and certificate chain on real EZGH. It runs only
// with TF_ACC=1 and EZGH_LIVE=1, never in CI; use a test organization and a domain you own.
func TestLiveCertificate(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_LIVE") != "1" {
		t.Skip("set TF_ACC=1 and EZGH_LIVE=1 to run against real EZGH")
	}
	project, domain := os.Getenv("EZGHCLOUD_TEST_PROJECT_ID"), os.Getenv("EZGHCLOUD_TEST_DOMAIN")
	if project == "" || domain == "" {
		t.Fatal("EZGHCLOUD_TEST_PROJECT_ID and EZGHCLOUD_TEST_DOMAIN are required")
	}
	config := fmt.Sprintf(`
resource "ezghcloud_dns_zone" "live" {
 project_id = %[1]q
 domain = %[2]q
}
resource "ezghcloud_certificate_domain_authorization" "live" {
 domain_name = %[2]q
 dns_zone_id = ezghcloud_dns_zone.live.id
}
resource "ezghcloud_certificate" "live" {
 project_id = %[1]q
 name = "tf-live"
 dns_names = [%[2]q]
 domain_authorization_ids = [ezghcloud_certificate_domain_authorization.live.id]
}
`, project, domain)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{{Config: config}}})
}
