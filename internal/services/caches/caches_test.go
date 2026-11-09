package caches

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func schemaOf(r resource.Resource) resource.SchemaResponse {
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr
}

// replaces is whether a change to the string attribute replaces the resource.
func replaces(t *testing.T, s schema.Schema, name string) bool {
	t.Helper()
	a, ok := s.Attributes[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s isn't a string attribute", name)
	}
	for _, m := range a.PlanModifiers {
		if strings.Contains(m.Description(context.Background()), "destroy and recreate") {
			return true
		}
	}
	return false
}

func TestCacheSchemaReplacesOnlyWhatMoves(t *testing.T) {
	s := schemaOf(&cacheResource{}).Schema
	for _, name := range []string{"engine", "mode", "subnet_id", "project_id"} {
		if !replaces(t, s, name) {
			t.Errorf("%s doesn't force replacement", name)
		}
	}
	for _, name := range []string{"engine_version", "instance_type", "name", "maintenance_window_utc"} {
		if replaces(t, s, name) {
			t.Errorf("%s forces replacement", name)
		}
	}
	for _, name := range []string{"endpoint_hostname", "endpoint_port", "endpoint_plaintext_port", "status", "id"} {
		if !s.Attributes[name].IsComputed() || s.Attributes[name].IsOptional() {
			t.Errorf("%s isn't computed only", name)
		}
	}
	for name := range s.Attributes {
		if strings.Contains(name, "password") {
			t.Errorf("%s holds a password", name)
		}
	}
}

func TestCacheUserSchema(t *testing.T) {
	s := schemaOf(&userResource{}).Schema
	for _, name := range []string{"name", "cache_id", "project_id"} {
		if !replaces(t, s, name) {
			t.Errorf("%s doesn't force replacement", name)
		}
	}
	if replaces(t, s, "preset") {
		t.Error("preset forces replacement")
	}
	if a := s.Attributes["secret_id"]; !a.IsComputed() || a.IsOptional() || a.IsRequired() {
		t.Error("secret_id isn't computed only")
	}
	for name, a := range s.Attributes {
		if strings.Contains(name, "password") || a.IsSensitive() {
			t.Errorf("%s holds a secret", name)
		}
	}
}

func available() cacheModel {
	m := cacheModel{}
	m.ID, m.OrganizationID, m.ProjectID, m.ResourceName, m.Name = types.StringValue("cch_123456789abc"), types.StringValue("org_test"), types.StringValue("prj_test"), types.StringValue("rn"), types.StringValue("wp")
	m.Description, m.Tags, m.Engine, m.EngineVersion, m.Mode = types.StringNull(), types.MapValueMust(types.StringType, nil), types.StringValue("valkey"), types.StringValue("9.1"), types.StringValue("persistent")
	m.InstanceType, m.Standby, m.StorageSizeGiB, m.StorageIOPS, m.StorageThroughputMBps = types.StringValue("gp-1.small"), types.BoolValue(false), types.Int64Value(20), types.Int64Value(3000), types.Int64Value(125)
	m.VPCID, m.SubnetID, m.AllowedSourceCIDRs, m.PlaintextPort = types.StringValue("vpc_a"), types.StringValue("sbn_123456789abc"), types.ListValueMust(types.StringType, nil), types.BoolNull()
	m.EndpointHostname, m.EndpointPort, m.EndpointPlaintextPort = types.StringNull(), types.Int64Null(), types.Int64Null()
	m.MaintenanceWindowUTC, m.Parameters, m.DeletionProtection, m.Status = types.StringValue("sun:05:00-sun:05:30"), types.MapNull(types.StringType), types.BoolValue(false), types.StringValue("available")
	m.OperationID, m.OperationTimeout = types.StringValue(""), types.StringValue("1m")
	return m
}

func asState(t *testing.T, r resource.Resource, m any) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: schemaOf(r).Schema}
	if d := s.Set(context.Background(), m); d.HasError() {
		t.Fatal(d)
	}
	return s
}

func modifyPlan(t *testing.T, state *cacheModel, plan cacheModel) resource.ModifyPlanResponse {
	t.Helper()
	r := &cacheResource{}
	p := asState(t, r, &plan)
	req := resource.ModifyPlanRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}, Config: tfsdk.Config{Schema: p.Schema, Raw: p.Raw}, State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
	if state != nil {
		req.State = asState(t, r, state)
	}
	resp := resource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(context.Background(), req, &resp)
	return resp
}

func TestCachePlanRefusesADowngradeAndShrinking(t *testing.T) {
	old := available()
	up := old
	up.EngineVersion = types.StringValue("9.10")
	if resp := modifyPlan(t, &old, up); resp.Diagnostics.HasError() {
		t.Errorf("upgrade: %v", resp.Diagnostics)
	}
	down := old
	down.EngineVersion = types.StringValue("8.1")
	if resp := modifyPlan(t, &old, down); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "one-way") {
		t.Errorf("downgrade: %v", resp.Diagnostics)
	}
	small := old
	small.StorageSizeGiB = types.Int64Value(10)
	if resp := modifyPlan(t, &old, small); !resp.Diagnostics.HasError() {
		t.Error("shrinking storage wasn't refused")
	}
	// A replacement (Valkey 9.1 to Memcached 1.6) makes a new cache: no upgrade checks.
	r := &cacheResource{}
	swap := old
	swap.Engine, swap.EngineVersion, swap.Mode, swap.StorageSizeGiB, swap.StorageIOPS, swap.StorageThroughputMBps = types.StringValue("memcached"), types.StringValue("1.6"), types.StringValue("memory"), types.Int64Null(), types.Int64Null(), types.Int64Null()
	st, pl := asState(t, r, &old), asState(t, r, &swap)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: pl.Schema, Raw: pl.Raw}, RequiresReplace: path.Paths{path.Root("engine")}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: st, Plan: tfsdk.Plan{Schema: pl.Schema, Raw: pl.Raw}, Config: tfsdk.Config{Schema: pl.Schema, Raw: pl.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("replacement: %v", resp.Diagnostics)
	}
}

func TestCachePlanChecksModeAndStorage(t *testing.T) {
	persistent := available()
	persistent.StorageSizeGiB = types.Int64Null()
	if resp := modifyPlan(t, nil, persistent); !resp.Diagnostics.HasError() {
		t.Error("persistent without storage wasn't refused")
	}
	memory := available()
	memory.Mode = types.StringValue("memory")
	if resp := modifyPlan(t, nil, memory); !resp.Diagnostics.HasError() {
		t.Error("memory with storage wasn't refused")
	}
	memcached := available()
	memcached.Engine, memcached.Mode, memcached.StorageSizeGiB = types.StringValue("memcached"), types.StringValue("persistent"), types.Int64Value(20)
	if resp := modifyPlan(t, nil, memcached); !resp.Diagnostics.HasError() {
		t.Error("persistent Memcached wasn't refused")
	}
}

func TestCacheBodies(t *testing.T) {
	ctx := context.Background()
	m := available()
	m.Mode, m.StorageSizeGiB, m.StorageIOPS, m.StorageThroughputMBps = types.StringValue("memory"), types.Int64Null(), types.Int64Unknown(), types.Int64Unknown()
	m.MaintenanceWindowUTC, m.AllowedSourceCIDRs = types.StringUnknown(), types.ListUnknown(types.StringType)
	body, d := createBody(ctx, m)
	if d.HasError() {
		t.Fatal(d)
	}
	want := map[string]any{"name": "wp", "tags": map[string]string{}, "engine": "valkey", "engineVersion": "9.1", "mode": "memory", "instanceType": "gp-1.small", "standby": false, "subnet": "sbn_123456789abc", "deletionProtection": false}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("valkey create %v", body)
	}
	m.Engine, m.PlaintextPort = types.StringValue("memcached"), types.BoolUnknown()
	if body, _ = createBody(ctx, m); body["mode"] != nil || body["plaintextPort"] != nil || body["storage"] != nil {
		t.Errorf("memcached create %v", body)
	}
	old := available()
	next := old
	next.Standby, next.StorageSizeGiB = types.BoolValue(true), types.Int64Value(40)
	next.Parameters = types.MapValueMust(types.StringType, map[string]attr.Value{"maxmemory-policy": types.StringValue("volatile-lru")})
	body, _ = updateBody(ctx, next, old)
	if body["name"] != nil || body["deletionProtection"] != nil || body["tags"] != nil || body["standby"] != true || !reflect.DeepEqual(body["storage"], map[string]any{"sizeGiB": int64(40)}) || !reflect.DeepEqual(body["parameters"], map[string]string{"maxmemory-policy": "volatile-lru"}) || body["engineVersion"] != nil {
		t.Errorf("update %v", body)
	}
}

// The API takes engineVersion on its own: Update sends the settings, then the upgrade.
func TestCacheUpdateUpgradesOnItsOwn(t *testing.T) {
	var mu sync.Mutex
	var patches []map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH":
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			mu.Lock()
			patches = append(patches, b)
			mu.Unlock()
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"running","target":{"resourceName":"rn:cch_123456789abc"}}`))
		case strings.Contains(r.URL.Path, "/operations/"):
			_, _ = w.Write([]byte(`{"id":"op1","status":"succeeded"}`))
		default:
			_, _ = w.Write([]byte(`{"slug":"cch_123456789abc","engine":"valkey","engineVersion":"9.1","mode":"persistent","status":"available","storage":{"sizeGiB":20},"maintenance":{"windowUtc":"sun:05:00-sun:05:30"}}`))
		}
	}))
	t.Cleanup(s.Close)
	r := &cacheResource{client: client.New(client.Config{OrganizationID: "org_test", Token: "t", Endpoints: map[string]string{"caches": s.URL, "orgs": s.URL}})}
	old := available()
	old.EngineVersion = types.StringValue("8.1")
	next := available()
	next.Standby = types.BoolValue(true)
	st, pl := asState(t, r, &old), asState(t, r, &next)
	resp := resource.UpdateResponse{State: st}
	r.Update(context.Background(), resource.UpdateRequest{State: st, Plan: tfsdk.Plan{Schema: pl.Schema, Raw: pl.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if len(patches) != 2 || !reflect.DeepEqual(patches[0], map[string]any{"standby": true}) || !reflect.DeepEqual(patches[1], map[string]any{"engineVersion": "9.1"}) {
		t.Errorf("%v", patches)
	}
	var version types.String
	resp.State.GetAttribute(context.Background(), path.Root("engine_version"), &version)
	if version.ValueString() != "9.1" {
		t.Errorf("state %s", version)
	}
}
