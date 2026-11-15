package warehouse_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

// fakeAPI is warehouse-api as the provider sees it: every write answers 202 with an operation that
// has already succeeded. Objects are kept by their escaped path. The database "events" exists from
// the start, as if made outside Terraform, and the warehouse's deletion protection refuses database
// deletes as the API does.
type fakeAPI struct {
	mu       sync.Mutex
	objects  map[string]map[string]any
	requests []string
	bodies   map[string][]map[string]any
	ops      int
}

const prefix = "/v1/organizations/org_test/projects/prj_test/warehouses"
const whID = "whs_wwwwwwwwwwww"

func newFake() *fakeAPI {
	return &fakeAPI{objects: map[string]map[string]any{}, bodies: map[string][]map[string]any{}}
}

func (f *fakeAPI) accepted(w http.ResponseWriter, resourceName string) {
	f.ops++
	w.Header().Set("Location", fmt.Sprintf("/v1/organizations/org_test/operations/op-%d", f.ops))
	w.WriteHeader(202)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("op-%d", f.ops), "status": "running", "target": map[string]any{"resourceName": resourceName}})
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	f.requests = append(f.requests, r.Method+" "+p)
	w.Header().Set("Content-Type", "application/json")
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if r.Method != "GET" {
		f.bodies[r.Method+" "+p] = append(f.bodies[r.Method+" "+p], body)
	}
	wh := prefix + "/" + whID
	switch {
	case p == "/v1/organizations/org_test/projects/prj_test":
		_, _ = w.Write([]byte(`{"id":"prj_test"}`))
	case strings.HasPrefix(p, "/v1/organizations/org_test/operations/"):
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	case r.Method == "POST" && p == prefix:
		f.objects[wh] = warehouse(body)
		f.objects[wh+"/users/app"] = map[string]any{"name": "app", "status": "available", "secret": "sec_app", "settingsProfile": "default", "permissions": map[string]any{"preset": "admin", "grants": []any{}}}
		f.objects[wh+"/databases/events"] = map[string]any{"name": "events"}
		f.accepted(w, "ezgh:us-west-1:org_test:prj_test:"+whID)
	case r.Method == "POST" && strings.HasSuffix(p, "/users"):
		name, _ := body["name"].(string)
		perms, _ := body["permissions"].(map[string]any)
		if perms == nil {
			perms = map[string]any{"preset": "readWrite"}
		}
		if perms["grants"] == nil {
			perms["grants"] = []any{}
		}
		profile, _ := body["settingsProfile"].(string)
		if profile == "" {
			profile = "default"
		}
		f.objects[p+"/"+name] = map[string]any{"name": name, "status": "available", "secret": "sec_" + name, "failure": nil, "permissions": perms, "settingsProfile": profile}
		f.accepted(w, p)
	case r.Method == "POST" && strings.HasSuffix(p, "/connections"):
		name, _ := body["name"].(string)
		for k, o := range f.objects {
			if strings.HasPrefix(k, p+"/") && body["type"] == "bucket" && o["target"] == body["target"] {
				refuse(w, 409, "already_exists", "The warehouse already has a connection to this bucket")
				return
			}
		}
		var secret any
		if body["type"] != "bucket" {
			secret = "sec_" + name
		}
		f.objects[p+"/"+name] = map[string]any{"warehouse": whID, "name": name, "type": body["type"], "target": body["target"], "access": body["access"], "topicPrefix": body["topicPrefix"],
			"database": body["database"], "secret": secret, "status": "available", "failure": nil, "createdAt": "2026-11-15T09:00:00.000Z"}
		f.accepted(w, p)
	case r.Method == "POST" && strings.HasSuffix(p, "/databases"):
		name, _ := body["name"].(string)
		if f.objects[p+"/"+name] != nil {
			refuse(w, 409, "already_exists", "A database named "+name+" already exists.")
			return
		}
		f.objects[p+"/"+name] = map[string]any{"name": name}
		f.accepted(w, p)
	case r.Method == "DELETE" && strings.Contains(p, "/databases/"):
		if f.objects[wh]["deletionProtection"] == true {
			refuse(w, 409, "deletion_protected", "Turn deletion protection off to delete a database.")
			return
		}
		if f.objects[p] == nil {
			refuse(w, 404, "not_found", "Not found")
			return
		}
		delete(f.objects, p)
		f.accepted(w, p)
	case f.objects[p] == nil:
		refuse(w, 404, "not_found", "Not found")
	case r.Method == "GET":
		_ = json.NewEncoder(w).Encode(f.objects[p])
	case r.Method == "PATCH":
		o := f.objects[p]
		for k, v := range body {
			switch k {
			case "storage":
				o["storage"].(map[string]any)["sizeGiB"] = v.(map[string]any)["sizeGiB"]
			case "backups":
				for bk, bv := range v.(map[string]any) {
					o["backups"].(map[string]any)[bk] = bv
				}
			case "plaintextPort":
				o[k] = v
				e := o["endpoint"].(map[string]any)
				e["plaintextHttpPort"], e["plaintextNativePort"] = nil, nil
				if v == true {
					e["plaintextHttpPort"], e["plaintextNativePort"] = 8123, 9000
				}
			default:
				o[k] = v
			}
		}
		if p == wh && o["keeper"] == nil && o["shards"].(float64)*o["replicas"].(float64) > 1 {
			o["keeper"] = "dedicated"
		}
		f.accepted(w, p)
	case r.Method == "DELETE":
		for k := range f.objects {
			if strings.HasPrefix(k, p) {
				delete(f.objects, k)
			}
		}
		f.accepted(w, p)
	}
}

func warehouse(body map[string]any) map[string]any {
	c := map[string]any{"slug": whID, "resourceName": "ezgh:us-west-1:org_test:prj_test:" + whID, "status": "available", "vpc": "vpc_aaaaaaaaaaaa", "allowedSourceCidrs": []any{"10.0.0.0/16"},
		"shards": float64(1), "replicas": float64(1), "keeper": nil, "maintenance": map[string]any{"windowUtc": "sun:05:00-sun:05:30"},
		"endpoint": map[string]any{"hostname": "wh-x.us-west-1.ezghcompute.com", "httpsPort": 8443, "nativePort": 9440, "plaintextHttpPort": nil, "plaintextNativePort": nil}}
	for k, v := range body {
		c[k] = v
	}
	c["description"] = body["description"]
	storage := body["storage"].(map[string]any)
	c["storage"] = map[string]any{"kind": "volume", "volumeType": "gp1", "sizeGiB": storage["sizeGiB"], "iops": 3000, "throughputMBps": 125}
	backups := map[string]any{"enabled": true, "retentionDays": 7, "windowUtc": "03:00-04:00"}
	if b, ok := body["backups"].(map[string]any); ok {
		for k, v := range b {
			backups[k] = v
		}
	}
	c["backups"] = backups
	if c["keeper"] == nil && c["shards"].(float64)*c["replicas"].(float64) > 1 {
		c["keeper"] = "dedicated"
	}
	return c
}

func config(version string, shards, replicas, size int, profile, protection string) string {
	return fmt.Sprintf(`
resource "ezghcloud_warehouse" "analytics" {
  project_id          = "prj_test"
  name                = "analytics"
  engine_version      = %[1]q
  shards              = %[2]d
  replicas            = %[3]d
  instance_type       = "gp-1.large"
  storage_size_gib    = %[4]d
  subnet_id           = "sbn_aaaaaaaaaaaa"
  deletion_protection = %[6]s
}
resource "ezghcloud_warehouse_user" "loader" {
  project_id       = "prj_test"
  warehouse_id     = ezghcloud_warehouse.analytics.id
  name             = "loader"
  preset           = "custom"
  grants           = [{ database = "My_DB", table = "a.b", privileges = ["SELECT", "INSERT"] }]
  settings_profile = %[5]q
}
resource "ezghcloud_warehouse_database" "mine" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "My_DB"
}
data "ezghcloud_warehouse" "analytics" {
  project_id = "prj_test"
  id         = ezghcloud_warehouse.analytics.id
}
`, version, shards, replicas, size, profile, protection)
}

func providerBlock(url string) string {
	return fmt.Sprintf(`provider "ezghcloud" {
  endpoints = { warehouse = %[1]q, orgs = %[1]q }
}
`, url)
}

func start(t *testing.T) (*fakeAPI, string) {
	t.Helper()
	t.Setenv("EZGHCLOUD_PREVIEW_WAREHOUSE", "1")
	t.Setenv("EZGHCLOUD_API_TOKEN", "t")
	t.Setenv("EZGHCLOUD_ORGANIZATION_ID", "org_test")
	f := newFake()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return f, providerBlock(s.URL)
}

// TestAccWarehouseLifecycle creates a 1 × 1 warehouse with a custom user and a database, then grows
// it to 2 × 2, upgrades it and grows its storage, changes the user in place, and destroys it all.
// Only the database resource calls the database routes, and destroying it deletes My_DB alone: the
// database events, made outside Terraform, is never touched.
func TestAccWarehouseLifecycle(t *testing.T) {
	f, pb := start(t)
	wh := prefix + "/" + whID
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			var databaseCalls []string
			for _, req := range f.requests {
				switch {
				case !strings.Contains(req, "/databases"):
				case req == "GET "+wh+"/databases/My_DB": // the database resource reading its own
				case strings.HasPrefix(req, "GET "):
					return fmt.Errorf("read a database it doesn't manage: %s", req)
				default:
					databaseCalls = append(databaseCalls, req)
				}
			}
			want := []string{"POST " + wh + "/databases", "DELETE " + wh + "/databases/My_DB"}
			if !reflect.DeepEqual(databaseCalls, want) {
				return fmt.Errorf("database calls %v, want %v", databaseCalls, want)
			}
			if got := f.bodies["DELETE "+wh+"/databases/My_DB"]; !reflect.DeepEqual(got, []map[string]any{{"name": "My_DB"}}) {
				return fmt.Errorf("database delete bodies %v", got)
			}
			if f.objects[wh] != nil {
				return fmt.Errorf("the warehouse is still there")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: pb + config("26.3", 1, 1, 100, "heavy", "false"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "id", whID),
				resource.TestCheckNoResourceAttr("ezghcloud_warehouse.analytics", "keeper"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "app_secret_id", "sec_app"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "endpoint_https_port", "8443"),
				resource.TestCheckNoResourceAttr("ezghcloud_warehouse.analytics", "endpoint_plaintext_http_port"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_user.loader", "secret_id", "sec_loader"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_user.loader", "grants.0.table", "a.b"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_database.mine", "id", whID+"/My_DB"),
				resource.TestCheckResourceAttr("data.ezghcloud_warehouse.analytics", "shards", "1"),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if got := f.bodies["POST "+wh+"/databases"]; !reflect.DeepEqual(got, []map[string]any{{"name": "My_DB"}}) {
						return fmt.Errorf("database create bodies %v", got)
					}
					return nil
				})},
			{Config: pb + config("26.8", 2, 2, 200, "light", "false"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "shards", "2"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "keeper", "dedicated"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "engine_version", "26.8"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_user.loader", "settings_profile", "light"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_user.loader", "secret_id", "sec_loader"),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					// Settings, then the shape, then the upgrade, each on its own.
					want := []map[string]any{{"storage": map[string]any{"sizeGiB": float64(200)}}, {"shards": float64(2), "replicas": float64(2)}, {"engineVersion": "26.8"}}
					if got := f.bodies["PATCH "+wh]; !reflect.DeepEqual(got, want) {
						return fmt.Errorf("warehouse patches %v", got)
					}
					if got := f.bodies["PATCH "+wh+"/users/loader"]; !reflect.DeepEqual(got, []map[string]any{{"settingsProfile": "light"}}) {
						return fmt.Errorf("user patches %v", got)
					}
					return nil
				})},
			// An import reads every attribute back, the app user's Secret included.
			{Config: pb + config("26.8", 2, 2, 200, "light", "false"), ResourceName: "ezghcloud_warehouse.analytics", ImportState: true, ImportStateVerify: true,
				ImportStateId: "org_test/prj_test/" + whID, ImportStateVerifyIgnore: []string{"operation_id", "operation_timeout"}},
			{Config: pb + config("26.8", 2, 2, 200, "light", "false"), ResourceName: "ezghcloud_warehouse_user.loader", ImportState: true, ImportStateVerify: true,
				ImportStateId: "org_test/prj_test/" + whID + "/loader"},
			// The Keeper mode on its own, and a user's grants in place.
			{Config: pb + strings.Replace(strings.Replace(config("26.8", 2, 2, 200, "light", "false"), `deletion_protection = false`, "deletion_protection = false\n  keeper = \"colocated\"", 1),
				`privileges = ["SELECT", "INSERT"]`, `privileges = ["SELECT"]`, 1), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "keeper", "colocated"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_user.loader", "grants.0.privileges.#", "1"),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if got := f.bodies["PATCH "+wh]; !reflect.DeepEqual(got[len(got)-1], map[string]any{"keeper": "colocated"}) {
						return fmt.Errorf("keeper patch %v", got)
					}
					want := map[string]any{"permissions": map[string]any{"preset": "custom", "grants": []any{map[string]any{"database": "My_DB", "table": "a.b", "privileges": []any{"SELECT"}}}}}
					if got := f.bodies["PATCH "+wh+"/users/loader"]; !reflect.DeepEqual(got[len(got)-1], want) {
						return fmt.Errorf("user patches %v", got)
					}
					return nil
				})},
			{Config: pb + strings.Replace(config("26.8", 2, 2, 200, "light", "false"), `preset           = "custom"`, `preset           = "readOnly"`, 1),
				ExpectError: regexp.MustCompile(`grants need preset custom`)},
			{Config: pb + strings.Replace(config("26.8", 2, 2, 200, "light", "false"), `["SELECT", "INSERT"]`, `["SELECT", "SELECT"]`, 1),
				ExpectError: regexp.MustCompile(`A privilege written twice`)},
			{Config: pb + config("26.8", 1, 2, 200, "light", "false"), ExpectError: regexp.MustCompile(`shards can't go down`)},
			{Config: pb + config("26.8", 2, 1, 200, "light", "false"), ExpectError: regexp.MustCompile(`replicas can't go down`)},
			{Config: pb + config("26.3", 2, 2, 200, "light", "false"), ExpectError: regexp.MustCompile(`engine_version can't go back`)},
			{Config: pb + config("26.8", 2, 2, 100, "light", "false"), ExpectError: regexp.MustCompile(`storage_size_gib can't go down`)},
		}})
}

// A database that already exists is an error naming terraform import, never adopted; destroying a
// database shows the API's deletion-protection refusal.
func TestDatabaseCreateConflictSaysImportAndDeleteShowsDeletionProtection(t *testing.T) {
	f, pb := start(t)
	existing := pb + config("26.8", 1, 1, 100, "default", "false") + `
resource "ezghcloud_warehouse_database" "events" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "events"
}
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: existing, ExpectError: regexp.MustCompile(`(?s)Database already exists.*terraform\s+import`)},
			{Config: pb + config("26.8", 1, 1, 100, "default", "true"), Check: func(*terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.objects[prefix+"/"+whID+"/databases/events"] == nil {
					return fmt.Errorf("the database events, made outside Terraform, was deleted")
				}
				return nil
			}},
			{Config: pb + config("26.8", 1, 1, 100, "default", "true"), Destroy: true, ExpectError: regexp.MustCompile(`(?s)Turn deletion protection off to delete a\s+database`)},
			{Config: pb + config("26.8", 1, 1, 100, "default", "false")},
		}})
}

func connectionsConfig(extra string) string {
	return config("26.8", 1, 1, 100, "default", "false") + `
resource "ezghcloud_warehouse_connection" "lake" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "lake"
  type         = "bucket"
  target_id    = "bkt_bbbbbbbbbbbb"
  access       = "read"
}
resource "ezghcloud_warehouse_connection" "clicks" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "clicks"
  type         = "kafka"
  target_id    = "hqc_kkkkkkkkkkkk"
  topic_prefix = "clicks."
}
` + extra
}

// Connections send each type's fields alone; a second connection to the same bucket shows the API's
// refusal; a broken one stays in state; a database dropped outside Terraform is created again.
func TestAccWarehouseConnections(t *testing.T) {
	f, pb := start(t)
	wh := prefix + "/" + whID
	second := `
resource "ezghcloud_warehouse_connection" "lake2" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "lake2"
  type         = "bucket"
  target_id    = "bkt_bbbbbbbbbbbb"
  access       = "readWrite"
}
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.objects[wh+"/connections/lake"] != nil || f.objects[wh+"/connections/clicks"] != nil {
				return fmt.Errorf("a connection is still there")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: pb + connectionsConfig(""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse_connection.lake", "id", whID+"/lake"),
				resource.TestCheckNoResourceAttr("ezghcloud_warehouse_connection.lake", "secret_id"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_connection.clicks", "secret_id", "sec_clicks"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_connection.clicks", "status", "available"),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					want := []map[string]any{
						{"name": "lake", "type": "bucket", "target": "bkt_bbbbbbbbbbbb", "access": "read"},
						{"name": "clicks", "type": "kafka", "target": "hqc_kkkkkkkkkkkk", "topicPrefix": "clicks."},
					}
					got := f.bodies["POST "+wh+"/connections"]
					if len(got) != 2 || !(reflect.DeepEqual(got, want) || reflect.DeepEqual(got, []map[string]any{want[1], want[0]})) {
						return fmt.Errorf("connection create bodies %v", got)
					}
					return nil
				})},
			{Config: pb + connectionsConfig(""), ResourceName: "ezghcloud_warehouse_connection.clicks", ImportState: true, ImportStateVerify: true,
				ImportStateId: "org_test/prj_test/" + whID + "/clicks"},
			{Config: pb + connectionsConfig(second), ExpectError: regexp.MustCompile(`already has a connection to this bucket`)},
			// The bucket went away: the connection is broken, still in state, and nothing is replaced.
			{PreConfig: func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				o := f.objects[wh+"/connections/lake"]
				o["status"], o["failure"] = "broken", map[string]any{"code": "target_gone", "message": "The connection's target is gone"}
			}, Config: pb + connectionsConfig(""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse_connection.lake", "status", "broken"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse_connection.lake", "failure_code", "target_gone"),
			)},
			// My_DB dropped outside Terraform: the next apply creates it again.
			{PreConfig: func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				delete(f.objects, wh+"/databases/My_DB")
			}, Config: pb + connectionsConfig(""), Check: func(*terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if n := len(f.bodies["POST "+wh+"/databases"]); n != 2 || f.objects[wh+"/databases/My_DB"] == nil {
					return fmt.Errorf("My_DB wasn't created again (%d creates)", n)
				}
				return nil
			}},
		}})
}

// Each type's fields, refused at plan on another type, as the API refuses them.
func TestConnectionConfigRefusedAtPlan(t *testing.T) {
	_, pb := start(t)
	conn := func(body string) string {
		return pb + config("26.8", 1, 1, 100, "default", "false") + `
resource "ezghcloud_warehouse_connection" "x" {
  project_id   = "prj_test"
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "x"
` + body + "}\n"
	}
	steps := []resource.TestStep{}
	for _, c := range []struct{ body, err string }{
		{"  type = \"bucket\"\n  target_id = \"bkt_bbbbbbbbbbbb\"\n", "A bucket connection needs access"},
		{"  type = \"kafka\"\n  target_id = \"hqc_kkkkkkkkkkkk\"\n  access = \"read\"\n", "access isn't for a kafka connection"},
		{"  type = \"database\"\n  target_id = \"dbi_dddddddddddd\"\n", "A database connection needs database"},
		{"  type = \"bucket\"\n  target_id = \"hqc_kkkkkkkkkkkk\"\n  access = \"read\"\n", "target is a bkt_"},
		{"  type = \"kafka\"\n  target_id = \"hqc_kkkkkkkkkkkk\"\n  topic_prefix = \"a/b\"\n", "Invalid topic_prefix"},
		{"  type = \"queue\"\n  target_id = \"hqc_kkkkkkkkkkkk\"\n", "type is bucket, kafka or database"},
	} {
		steps = append(steps, resource.TestStep{Config: conn(c.body), PlanOnly: true, ExpectError: regexp.MustCompile(regexp.QuoteMeta(c.err))})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: steps})
}
