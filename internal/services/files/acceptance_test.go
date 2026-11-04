package files_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// fixture is files-api over memory: creates and deletes answer 202 and their operations succeed at
// once; updates are synchronous, as on the API.
type fixture struct {
	mu      sync.Mutex
	objects map[string]map[string]any // by slug
	next    int
	calls   []string // METHOD collection[/slug] of every resource call
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{objects: map[string]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

var prefixes = map[string]string{"file-systems": "fsy_", "mount-targets": "fmt_", "access-points": "fap_"}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	fail := func(status int, code string) {
		send(status, map[string]any{"error": map[string]any{"code": code, "message": code}})
	}
	accepted := func(slug string) {
		w.Header().Set("Location", "/v1/organizations/org_test/operations/op-"+slug)
		send(202, map[string]any{"id": "op-" + slug, "status": "queued", "target": map[string]any{"id": "0193", "resourceName": "ezgh:files:us-west-1:org_test:prj_test/" + slug}})
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(parts) == 5 && parts[3] == "operations" {
		send(200, map[string]any{"id": parts[4], "status": "succeeded"})
		return
	}
	if len(parts) == 5 && parts[3] == "projects" {
		send(200, map[string]any{"id": "prj_test"})
		return
	}
	if len(parts) < 6 || prefixes[parts[5]] == "" {
		fail(404, "not_found")
		return
	}
	collection := parts[5]
	call := r.Method + " " + collection
	if len(parts) == 7 {
		call += "/" + parts[6]
	}
	f.calls = append(f.calls, call)
	if len(parts) == 6 && r.Method == "POST" {
		f.next++
		slug := fmt.Sprintf("%s%012d", prefixes[collection], f.next)
		obj := map[string]any{"id": "0193-uuid", "slug": slug, "resourceName": "ezgh:files:us-west-1:org_test:prj_test/" + slug, "name": slug,
			"description": nil, "tags": map[string]any{}, "region": "us-west-1", "status": "available", "failure": nil}
		for k, v := range body {
			obj[k] = v
		}
		switch collection {
		case "file-systems":
			obj["sizeBytes"], obj["limits"] = "0", map[string]any{"throughputMBps": 250, "metadataOpsPerSecond": 2000}
			if _, ok := body["rootSquash"]; !ok {
				obj["rootSquash"] = true
			}
			if _, ok := body["requireTls"]; !ok {
				obj["requireTls"] = false
			}
			if _, ok := body["deletionProtection"]; !ok {
				obj["deletionProtection"] = false
			}
		case "mount-targets":
			fs := f.objects[fmt.Sprint(body["fileSystem"])]
			if fs == nil {
				fail(404, "not_found")
				return
			}
			obj["vpc"], obj["ipv4Address"], obj["hostname"] = fs["vpc"], "10.0.1.10", slug+".files.us-west-1.ezghcloud.com"
			if _, ok := body["allowedSourceCidrs"]; !ok {
				obj["allowedSourceCidrs"] = []any{"10.0.0.0/16"}
			}
		case "access-points":
			if _, ok := body["rootDirectory"]; !ok {
				obj["rootDirectory"] = map[string]any{"path": "/", "creationInfo": nil}
			} else if rd := body["rootDirectory"].(map[string]any); rd["creationInfo"] == nil {
				rd["creationInfo"] = nil
			}
			if p, ok := obj["posixUser"].(map[string]any); ok && p["secondaryGids"] == nil {
				p["secondaryGids"] = []any{}
			}
			if _, ok := body["posixUser"]; !ok {
				obj["posixUser"] = nil
			}
		}
		f.objects[slug] = obj
		accepted(slug)
		return
	}
	obj := f.objects[parts[len(parts)-1]]
	if len(parts) != 7 || obj == nil || !strings.HasPrefix(parts[6], prefixes[collection]) {
		fail(404, "not_found")
		return
	}
	switch r.Method {
	case "GET":
		send(200, obj)
	case "PATCH":
		for _, k := range []string{"vpc", "fileSystem", "subnet", "rootDirectory", "posixUser"} {
			if _, ok := body[k]; ok {
				fail(400, "immutable_field")
				return
			}
		}
		for k, v := range body {
			obj[k] = v
		}
		if body["description"] == "" {
			obj["description"] = nil
		}
		send(200, obj)
	case "DELETE":
		if collection == "file-systems" {
			if obj["deletionProtection"] == true {
				fail(409, "deletion_protected")
				return
			}
			for _, o := range f.objects {
				if o["fileSystem"] == parts[6] {
					fail(409, "file_system_in_use")
					return
				}
			}
		}
		delete(f.objects, parts[6])
		accepted(parts[6])
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, files = %[1]q }
}
`, f.URL)
}

func (f *fixture) patched(collection string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "PATCH "+collection+"/") {
			n++
		}
	}
	return n
}

func gone(f *fixture) func(*terraform.State) error {
	return func(*terraform.State) error {
		if len(f.objects) != 0 {
			return fmt.Errorf("left %v", f.objects)
		}
		return nil
	}
}

func action(name string, a plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, a)}}
}

const fileSystem = `
resource "ezghcloud_file_system" "shared" {
 project_id = "prj_test"
 name = %q
 vpc_id = "vpc_123456789abc"
 require_tls = %t
 tags = { team = "web" }
}
`

func TestAccFileSystemLifecycle(t *testing.T) {
	f := newFixture(t)
	config := func(name string, tls bool) string {
		return f.provider() + fmt.Sprintf(fileSystem, name, tls) + `
data "ezghcloud_file_system" "shared" {
 project_id = "prj_test"
 id = ezghcloud_file_system.shared.id
}
`
	}
	r := "ezghcloud_file_system.shared"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: gone(f), Steps: []resource.TestStep{
		{Config: config("shared", false), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "fsy_000000000001"),
			resource.TestCheckResourceAttr(r, "vpc_id", "vpc_123456789abc"),
			resource.TestCheckResourceAttr(r, "size_bytes", "0"),
			resource.TestCheckResourceAttr(r, "root_squash", "true"),
			resource.TestCheckResourceAttr(r, "require_tls", "false"),
			resource.TestCheckResourceAttr(r, "throughput_mbps", "250"),
			resource.TestCheckResourceAttr(r, "status", "available"),
			resource.TestCheckResourceAttr("data.ezghcloud_file_system.shared", "name", "shared"),
			resource.TestCheckResourceAttr("data.ezghcloud_file_system.shared", "tags.team", "web"))},
		{Config: config("shared-files", true), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "fsy_000000000001"),
			resource.TestCheckResourceAttr(r, "name", "shared-files"),
			resource.TestCheckResourceAttr(r, "require_tls", "true"),
			func(*terraform.State) error {
				if n := f.patched("file-systems"); n != 1 {
					return fmt.Errorf("%d PATCHes", n)
				}
				return nil
			})},
		{Config: strings.Replace(config("shared-files", true), "vpc_123456789abc", "vpc_999999999999", 1), ExpectError: regexp.MustCompile(`vpc_id can't change`)},
		{ResourceName: r, ImportState: true, ImportStateId: "org_test/prj_test/fsy_000000000001", ImportStateVerify: true},
	}})
}

func TestAccMountTargetLifecycle(t *testing.T) {
	f := newFixture(t)
	config := func(cidrs string) string {
		return f.provider() + fmt.Sprintf(fileSystem, "shared", false) + fmt.Sprintf(`
resource "ezghcloud_mount_target" "web" {
 project_id = "prj_test"
 file_system_id = ezghcloud_file_system.shared.id
 subnet_id = "sbn_123456789abc"
 %s
}
`, cidrs)
	}
	r := "ezghcloud_mount_target.web"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: gone(f), Steps: []resource.TestStep{
		{Config: config(""), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "fmt_000000000002"),
			resource.TestCheckResourceAttr(r, "name", "fmt_000000000002"),
			resource.TestCheckResourceAttr(r, "file_system_id", "fsy_000000000001"),
			resource.TestCheckResourceAttr(r, "vpc_id", "vpc_123456789abc"),
			resource.TestCheckResourceAttr(r, "hostname", "fmt_000000000002.files.us-west-1.ezghcloud.com"),
			resource.TestCheckResourceAttr(r, "ip_address", "10.0.1.10"),
			resource.TestCheckResourceAttr(r, "allowed_source_cidrs.0", "10.0.0.0/16"))},
		{Config: config(`allowed_source_cidrs = ["10.0.1.0/24", "10.0.2.0/24"]`), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(r, "id", "fmt_000000000002"),
			resource.TestCheckResourceAttr(r, "allowed_source_cidrs.#", "2"),
			resource.TestCheckResourceAttr(r, "allowed_source_cidrs.1", "10.0.2.0/24"),
			resource.TestCheckResourceAttr(r, "hostname", "fmt_000000000002.files.us-west-1.ezghcloud.com"))},
		{Config: strings.Replace(config(`allowed_source_cidrs = ["10.0.1.0/24", "10.0.2.0/24"]`), "sbn_123456789abc", "sbn_999999999999", 1),
			ConfigPlanChecks: action(r, plancheck.ResourceActionDestroyBeforeCreate),
			Check:            resource.TestCheckResourceAttr(r, "subnet_id", "sbn_999999999999")},
		{ResourceName: r, ImportState: true, ImportStateId: "org_test/prj_test/fmt_000000000003", ImportStateVerify: true},
	}})
}

func TestAccAccessPointLifecycle(t *testing.T) {
	f := newFixture(t)
	config := func(name, path string, uid int) string {
		return f.provider() + fmt.Sprintf(fileSystem, "shared", false) + fmt.Sprintf(`
resource "ezghcloud_access_point" "wp" {
 project_id = "prj_test"
 file_system_id = ezghcloud_file_system.shared.id
 name = %q
 root_directory = {
  path = %q
  creation_info = { owner_uid = 33, owner_gid = 33, permissions = "755" }
 }
 posix_user = { uid = %d, gid = 33 }
}
resource "ezghcloud_access_point" "root" {
 project_id = "prj_test"
 file_system_id = ezghcloud_file_system.shared.id
 name = "root"
}
`, name, path, uid)
	}
	r := "ezghcloud_access_point.wp"
	replaced := action(r, plancheck.ResourceActionDestroyBeforeCreate)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: gone(f), Steps: []resource.TestStep{
		{Config: strings.Replace(config("wp-content", "/wordpress/wp-content", 33), "gid = 33 }", "gid = 33, secondary_gids = [4] }", 1),
			ExpectError: regexp.MustCompile(`Secondary groups aren't supported`)},
		{Config: config("wp-content", "/wordpress/wp-content", 33), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttrPair(r, "file_system_id", "ezghcloud_file_system.shared", "id"),
			resource.TestCheckResourceAttr(r, "root_directory.path", "/wordpress/wp-content"),
			resource.TestCheckResourceAttr(r, "root_directory.creation_info.permissions", "755"),
			resource.TestCheckResourceAttr(r, "posix_user.uid", "33"),
			resource.TestCheckResourceAttr(r, "posix_user.secondary_gids.#", "0"),
			resource.TestCheckResourceAttr("ezghcloud_access_point.root", "root_directory.path", "/"),
			resource.TestCheckNoResourceAttr("ezghcloud_access_point.root", "posix_user"))},
		{Config: config("wordpress", "/wordpress/wp-content", 33), ConfigPlanChecks: action(r, plancheck.ResourceActionUpdate),
			Check: resource.TestCheckResourceAttr(r, "name", "wordpress")},
		{Config: config("wordpress", "/wordpress/uploads", 33), ConfigPlanChecks: replaced,
			Check: resource.TestCheckResourceAttr(r, "root_directory.path", "/wordpress/uploads")},
		{Config: config("wordpress", "/wordpress/uploads", 1000), ConfigPlanChecks: replaced,
			Check: resource.TestCheckResourceAttr(r, "posix_user.uid", "1000")},
		{ResourceName: r, ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
			return "org_test/prj_test/" + s.RootModule().Resources[r].Primary.ID, nil
		}, ImportStateVerify: true},
		{ResourceName: "ezghcloud_access_point.root", ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
			return "org_test/prj_test/" + s.RootModule().Resources["ezghcloud_access_point.root"].Primary.ID, nil
		}, ImportStateVerify: true},
	}})
}
