package databases_test

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

// fixture is databases-api over memory: every operation succeeds at once.
type fixture struct {
	mu      sync.Mutex
	objects map[string]map[string]any
	deletes []string // the finalBackup query of each database delete
	patches int
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{objects: map[string]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve(t)))
	t.Cleanup(f.Close)
	return f
}
func merge(dst map[string]any, key string, src any) {
	if m, ok := src.(map[string]any); ok {
		if into, ok := dst[key].(map[string]any); ok {
			for k, v := range m {
				into[k] = v
			}
			return
		}
	}
	dst[key] = src
}
func (f *fixture) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		accepted := func(op, name string) {
			w.Header().Set("Location", "/v1/organizations/org_test/operations/"+op)
			w.WriteHeader(202)
			send(map[string]any{"id": op, "status": "pending", "target": map[string]any{"id": "0193", "resourceName": "ezgh:databases:us-west-1:org_test:prj_test/" + name}})
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		last := parts[len(parts)-1]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case parts[len(parts)-2] == "operations":
			send(map[string]any{"id": last, "status": "succeeded"})
		case len(parts) == 5 && parts[3] == "projects":
			send(map[string]any{"id": "project"})
		case r.Method == "POST" && last == "databases":
			obj := map[string]any{"slug": "dbi_123456789abc", "resourceName": "ezgh:databases:us-west-1:org_test:prj_test/dbi_123456789abc", "vpc": "vpc_123456789abc", "adminSecret": "sec_123456789abc", "status": "available",
				"endpoint": map[string]any{"hostname": "db-123456789abc.us-west-1.ezghcompute.com", "port": 5432}, "description": nil, "allowedSourceCidrs": []any{"10.0.0.0/16"},
				"storage": map[string]any{"volumeType": "gp1", "iops": 3000, "throughputMBps": 125}, "backup": map[string]any{"retentionDays": 7, "windowUtc": "03:00-03:30"}, "maintenance": map[string]any{"windowUtc": "sun:04:00-sun:04:30"}}
			for k, v := range body {
				if k == "source" { // the source fixes the engine
					obj["engine"], obj["engineVersion"] = "postgresql", "18"
					continue
				}
				merge(obj, k, v)
			}
			obj["subnet"] = body["subnet"]
			if body["description"] == "" {
				obj["description"] = nil
			}
			params, _ := obj["parameters"].(map[string]any)
			if params == nil {
				params = map[string]any{}
			}
			params["shared_buffers"] = "128MB" // a default the API adds
			obj["parameters"] = params
			f.objects["dbi_123456789abc"] = obj
			accepted("create-db", "dbi_123456789abc")
		case r.Method == "POST" && last == "backups":
			if f.objects[parts[len(parts)-2]] == nil {
				w.WriteHeader(404)
				return
			}
			f.objects["dbb_123456789abc"] = map[string]any{"slug": "dbb_123456789abc", "resourceName": "ezgh:databases:us-west-1:org_test:prj_test/dbb_123456789abc", "database": parts[len(parts)-2], "kind": "manual", "engine": "postgresql", "engineVersion": "18", "sizeBytes": "1048576", "status": "available", "createdAt": "2026-11-02T10:00:00.000Z", "expiresAt": nil}
			accepted("create-backup", "dbb_123456789abc")
		case f.objects[last] != nil && r.Method == "GET":
			send(f.objects[last])
		case f.objects[last] != nil && r.Method == "PATCH":
			f.patches++
			obj := f.objects[last]
			for k, v := range body {
				merge(obj, k, v)
			}
			if body["description"] == "" {
				obj["description"] = nil // the API shows an empty description as null
			}
			accepted("modify", last)
		case f.objects[last] != nil && r.Method == "DELETE":
			if strings.HasPrefix(last, "dbb_") {
				f.objects[last]["status"] = "deleting"
				send(f.objects[last])
				delete(f.objects, last)
				return
			}
			f.deletes = append(f.deletes, r.URL.Query().Get("finalBackup"))
			delete(f.objects, last)
			accepted("delete", last)
		default:
			w.WriteHeader(404)
			send(map[string]any{"error": map[string]any{"code": "not_found", "message": "missing"}})
		}
	}
}
func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, databases = %[1]q }
}
`, f.URL)
}

func TestAccDatabaseLifecycle(t *testing.T) {
	f := newFixture(t)
	config := func(instanceType string, size int, version string, workMem ...string) string {
		mem := "8MB"
		if len(workMem) > 0 {
			mem = workMem[0]
		}
		return f.provider() + fmt.Sprintf(`
resource "ezghcloud_database" "orders" {
 project_id = "prj_test"
 name = "orders"
 description = ""
 engine = "postgresql"
 engine_version = %[3]q
 instance_type = %[1]q
 storage_size_gib = %[2]d
 subnet_id = "sbn_123456789abc"
 parameters = { work_mem = %[4]q }
}
data "ezghcloud_database" "orders" {
 project_id = "prj_test"
 id = ezghcloud_database.orders.id
}
`, instanceType, size, version, mem)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if len(f.deletes) != 1 || f.deletes[0] != "true" {
				return fmt.Errorf("final backups asked for: %v", f.deletes)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("gs-1.large", 20, "18"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "id", "dbi_123456789abc"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "admin_secret_id", "sec_123456789abc"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "endpoint_port", "5432"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "storage_iops", "3000"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "parameters.%", "1"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "final_backup_on_destroy", "true"),
				resource.TestCheckResourceAttr("data.ezghcloud_database.orders", "parameters.shared_buffers", "128MB"),
				resource.TestCheckResourceAttr("data.ezghcloud_database.orders", "vpc_id", "vpc_123456789abc"))},
			{Config: config("gs-1.xlarge", 40, "18"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "instance_type", "gs-1.xlarge"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "storage_size_gib", "40"),
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "id", "dbi_123456789abc"))},
			// A parameter Terraform never managed survives a change to one it does.
			{Config: config("gs-1.xlarge", 40, "18", "16MB"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database.orders", "parameters.work_mem", "16MB"),
				resource.TestCheckResourceAttr("data.ezghcloud_database.orders", "parameters.shared_buffers", "128MB"))},
			{Config: config("gs-1.xlarge", 40, "17"), ExpectError: regexp.MustCompile(`blue/green deployment`)},
			{Config: config("gs-1.xlarge", 20, "18"), ExpectError: regexp.MustCompile(`Storage can't shrink`)},
			// A blue/green switchover keeps the dbi_ and endpoint and moves the version: set it, nothing is replaced.
			{PreConfig: func() { f.mu.Lock(); f.objects["dbi_123456789abc"]["engineVersion"] = "19"; f.mu.Unlock() },
				Config: config("gs-1.xlarge", 40, "19", "16MB"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ezghcloud_database.orders", plancheck.ResourceActionNoop)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ezghcloud_database.orders", "id", "dbi_123456789abc"),
					resource.TestCheckResourceAttr("ezghcloud_database.orders", "engine_version", "19"),
					resource.TestCheckResourceAttr("ezghcloud_database.orders", "endpoint_hostname", "db-123456789abc.us-west-1.ezghcompute.com"))},
			{ResourceName: "ezghcloud_database.orders", ImportState: true, ImportStateId: "org_test/prj_test/dbi_123456789abc", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id", "parameters", "description"}},
		}})
}

// The restore source is set once: removing or changing it updates state only, never the database.
func TestAccDatabaseRestoreSourceSetOnce(t *testing.T) {
	f := newFixture(t)
	config := func(source string) string {
		return f.provider() + fmt.Sprintf(`
resource "ezghcloud_database" "restored" {
 project_id = "prj_test"
 name = "restored"
 instance_type = "gs-1.large"
 storage_size_gib = 20
 subnet_id = "sbn_123456789abc"
 final_backup_on_destroy = false
 %s
}
`, source)
	}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ezghcloud_database.restored", plancheck.ResourceActionUpdate)}}
	kept := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("ezghcloud_database.restored", "id", "dbi_123456789abc"),
		func(*terraform.State) error {
			if f.patches != 0 || len(f.deletes) != 0 {
				return fmt.Errorf("source change reached the API: %d patches, deletes %v", f.patches, f.deletes)
			}
			return nil
		})
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config(`source_backup_id = "dbb_123456789abc"`), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database.restored", "source_backup_id", "dbb_123456789abc"),
				resource.TestCheckResourceAttr("ezghcloud_database.restored", "engine_version", "18"))},
			{Config: config(`source_backup_id = "dbb_999999999999"`), ConfigPlanChecks: update, Check: kept},
			{Config: config(""), ConfigPlanChecks: update, Check: resource.ComposeAggregateTestCheckFunc(kept,
				resource.TestCheckNoResourceAttr("ezghcloud_database.restored", "source_backup_id"))},
		}})
}

func TestAccDatabaseBackupLifecycle(t *testing.T) {
	f := newFixture(t)
	config := f.provider() + `
resource "ezghcloud_database" "orders" {
 project_id = "prj_test"
 name = "orders"
 engine = "postgresql"
 engine_version = "18"
 instance_type = "gs-1.large"
 storage_size_gib = 20
 subnet_id = "sbn_123456789abc"
 final_backup_on_destroy = false
}
resource "ezghcloud_database_backup" "before" {
 project_id = "prj_test"
 database_id = ezghcloud_database.orders.id
}
data "ezghcloud_database_backup" "before" {
 project_id = "prj_test"
 id = ezghcloud_database_backup.before.id
}
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if len(f.deletes) != 1 || f.deletes[0] != "false" || len(f.objects) != 0 {
				return fmt.Errorf("left %v, final backups %v", f.objects, f.deletes)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database_backup.before", "id", "dbb_123456789abc"),
				resource.TestCheckResourceAttr("ezghcloud_database_backup.before", "kind", "manual"),
				resource.TestCheckNoResourceAttr("ezghcloud_database_backup.before", "expires_at"),
				resource.TestCheckResourceAttr("data.ezghcloud_database_backup.before", "database_id", "dbi_123456789abc"),
				resource.TestCheckResourceAttr("data.ezghcloud_database_backup.before", "size_bytes", "1048576"))},
			{ResourceName: "ezghcloud_database_backup.before", ImportState: true, ImportStateId: "org_test/prj_test/dbb_123456789abc", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id"}},
		}})
}
