package databases

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func testClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return client.New(client.Config{OrganizationID: "org_test", Token: "test", Endpoints: map[string]string{"databases": s.URL, "orgs": s.URL}})
}
func schemaOf(r resource.Resource) resource.SchemaResponse {
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr
}

// planned is a database as Terraform plans its create: what's left out is unknown.
func planned(ctx context.Context) databaseModel {
	m := databaseModel{}
	m.ID, m.OrganizationID, m.ResourceName, m.VPCID, m.Status = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	m.ProjectID, m.Name, m.Engine, m.EngineVersion = types.StringValue("prj_test"), types.StringValue("orders"), types.StringValue("postgresql"), types.StringValue("18")
	m.InstanceType, m.SubnetID = types.StringValue("gs-1.large"), types.StringValue("sbn_123456789abc")
	m.StorageVolumeType, m.StorageSizeGiB, m.StorageIOPS, m.StorageThroughputMBps = types.StringUnknown(), types.Int64Value(20), types.Int64Unknown(), types.Int64Unknown()
	m.AllowedSourceCIDRs, m.EndpointHostname, m.EndpointPort, m.AdminSecretID = types.ListUnknown(types.StringType), types.StringUnknown(), types.Int64Unknown(), types.StringUnknown()
	m.BackupRetentionDays, m.BackupWindowUTC, m.MaintenanceWindowUTC = types.Int64Unknown(), types.StringUnknown(), types.StringUnknown()
	m.Tags, m.Parameters = mapValue(ctx, nil), types.MapNull(types.StringType)
	m.LogExports, m.DeletionProtection = types.SetValueMust(types.StringType, nil), types.BoolValue(false)
	m.OperationID, m.OperationTimeout, m.FinalBackupOnDestroy = types.StringUnknown(), types.StringValue("1s"), types.BoolValue(true)
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
func modifyPlan(t *testing.T, state, plan databaseModel) resource.ModifyPlanResponse {
	t.Helper()
	r := &databaseResource{}
	s, p := asState(t, r, &state), asState(t, r, &plan)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	return resp
}
func available(ctx context.Context) databaseModel {
	m := planned(ctx)
	m.fromAPI(ctx, databaseView{Slug: "dbi_123456789abc", Engine: "postgresql", EngineVersion: "18", Status: "available"}, "org_test")
	m.StorageSizeGiB = types.Int64Value(20)
	return m
}

func TestDatabaseCreateRetainsIdentityOnFailedOperation(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"pending","target":{"id":"0193","resourceName":"ezgh:databases:us-west-1:org:prj/dbi_123456789abc","type":"database"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"op1","status":"failed","error":{"code":"failure","message":"failed"}}`))
	})
	r := &databaseResource{client: c}
	m := planned(ctx)
	p := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: p.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed operation was accepted")
	}
	if !resp.State.Raw.IsFullyKnown() {
		t.Fatal("state holds unknown values")
	}
	var got databaseModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueString() != "dbi_123456789abc" || got.OperationID.ValueString() != "op1" || got.Status.ValueString() != "creating" {
		t.Fatalf("identity lost: %#v", got)
	}
}

// A create that outlasts its wait isn't failed: an error would taint it, and the next apply would destroy it.
func TestDatabaseCreateTimeoutKeepsDatabase(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"pending","target":{"id":"0193","resourceName":"ezgh:databases:us-west-1:org:prj/dbi_123456789abc","type":"database"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"op1","status":"running"}`))
	})
	r := &databaseResource{client: c}
	m := planned(ctx) // operation_timeout 1s
	p := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: p.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatal(resp.Diagnostics)
	}
	var got databaseModel
	if d := resp.State.Get(ctx, &got); d.HasError() || got.Status.ValueString() != "creating" || got.OperationID.ValueString() != "op1" {
		t.Fatalf("%v %#v", d, got)
	}
}

func TestDatabaseShrinkRefused(t *testing.T) {
	ctx := context.Background()
	state, plan := available(ctx), available(ctx)
	plan.StorageSizeGiB = types.Int64Value(10)
	if resp := modifyPlan(t, state, plan); !resp.Diagnostics.HasError() {
		t.Fatal("shrink planned")
	}
	plan.StorageSizeGiB = types.Int64Value(40)
	if resp := modifyPlan(t, state, plan); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestDatabaseEngineVersionChangeRefusedAtPlan(t *testing.T) {
	ctx := context.Background()
	state, plan := available(ctx), available(ctx)
	plan.EngineVersion = types.StringValue("17")
	resp := modifyPlan(t, state, plan)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "blue/green deployment") {
		t.Fatalf("engine version change planned: %v", resp.Diagnostics)
	}
	plan.EngineVersion = types.StringUnknown()
	if resp := modifyPlan(t, state, plan); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestDatabaseRestoreSourceChecked(t *testing.T) {
	ctx := context.Background()
	r := &databaseResource{}
	plan := planned(ctx)
	plan.SourceBackupID, plan.SourceTime = types.StringValue("dbb_123456789abc"), types.StringValue("2026-11-02T10:00:00.000Z")
	p := asState(t, r, &plan)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(ctx), nil)}, Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("backup and time accepted together")
	}
}

func TestDatabaseUnknownRestoreSourceWaitsForApply(t *testing.T) {
	ctx := context.Background()
	r := &databaseResource{}
	plan := planned(ctx)
	plan.SourceDatabaseID, plan.SourceTime = types.StringValue("dbi_123456789abc"), types.StringUnknown()
	p := asState(t, r, &plan)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(ctx), nil)}, Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

// A restore source is set once: changing or removing it warns and never replaces the database.
func TestDatabaseRestoreSourceSetOnce(t *testing.T) {
	ctx := context.Background()
	for name, a := range schemaOf(&databaseResource{}).Schema.Attributes {
		if strings.HasPrefix(name, "source_") && len(a.(schema.StringAttribute).PlanModifiers) != 0 {
			t.Fatalf("%s has plan modifiers", name)
		}
	}
	state, plan := available(ctx), available(ctx)
	state.SourceBackupID = types.StringValue("dbb_123456789abc")
	resp := modifyPlan(t, state, plan)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("source removal: %v", resp.Diagnostics)
	}
	// Nothing replaces a database: each of these, known or not, is refused at plan.
	for _, field := range []*types.String{&plan.SubnetID, &plan.ProjectID, &plan.Engine, &plan.StorageVolumeType} {
		for _, v := range []types.String{types.StringValue("other"), types.StringUnknown()} {
			was := *field
			*field = v
			resp := modifyPlan(t, state, plan)
			*field = was
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "restore a backup or a point in time into a new database") {
				t.Fatalf("%v planned: %v", v, resp.Diagnostics)
			}
		}
	}
	// A source change isn't a replacement, so the version check still holds.
	plan.EngineVersion = types.StringValue("17")
	if resp := modifyPlan(t, state, plan); !resp.Diagnostics.HasError() {
		t.Fatal("engine version change planned beside a source change")
	}
}

func TestDatabaseStateHoldsNoPassword(t *testing.T) {
	for name := range schemaOf(&databaseResource{}).Schema.Attributes {
		if strings.Contains(name, "password") {
			t.Fatalf("schema has %s", name)
		}
	}
	m := databaseModel{}
	m.fromAPI(context.Background(), databaseView{Slug: "dbi_123456789abc", AdminSecret: "sec_123456789abc"}, "org_test")
	if m.AdminSecretID.ValueString() != "sec_123456789abc" {
		t.Fatal("admin secret not read")
	}
}

func TestDatabaseParametersReadOnlyConfiguredKeys(t *testing.T) {
	ctx := context.Background()
	m := databaseModel{}
	m.Parameters = mapValue(ctx, map[string]string{"work_mem": "8MB", "gone": "1"})
	m.fromAPI(ctx, databaseView{Slug: "dbi_123456789abc", Parameters: map[string]string{"work_mem": "16MB", "max_connections": "200"}}, "org_test")
	got := map[string]string{}
	m.Parameters.ElementsAs(ctx, &got, false)
	if len(got) != 1 || got["work_mem"] != "16MB" {
		t.Fatalf("parameters = %v", got)
	}
	m.Parameters = types.MapNull(types.StringType)
	m.fromAPI(ctx, databaseView{Slug: "dbi_123456789abc", Parameters: map[string]string{"max_connections": "200"}}, "org_test")
	if !m.Parameters.IsNull() {
		t.Fatal("unconfigured parameters read back")
	}
}

func TestDatabaseDeletionProtectionHonoured(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	r := &databaseResource{client: c}
	m := available(ctx)
	m.DeletionProtection = types.BoolValue(true)
	resp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: asState(t, r, &m)}, &resp)
	if !resp.Diagnostics.HasError() || calls != 0 {
		t.Fatalf("protected database deleted: calls=%d", calls)
	}
}

func TestOperationResourceIDUsesResourceName(t *testing.T) {
	id, err := operationResourceID([]byte(`{"id":"op","target":{"id":"0193","resourceName":"ezgh:databases:us-west-1:o:p/dbb_123456789abc"}}`))
	if err != nil || id != "dbb_123456789abc" {
		t.Fatalf("%q %v", id, err)
	}
	if _, err := operationResourceID([]byte(`{"id":"op","target":{"id":"0193"}}`)); err == nil {
		t.Fatal("accepted an operation without a resource name")
	}
}

func TestSchemasValidate(t *testing.T) {
	ctx := context.Background()
	for _, f := range Resources() {
		if d := schemaOf(f()).Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatal(d)
		}
	}
	for _, f := range DataSources() {
		var s datasource.SchemaResponse
		f().Schema(ctx, datasource.SchemaRequest{}, &s)
		if d := s.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatal(d)
		}
		if s.Schema.Attributes["operation_timeout"] != nil || !s.Schema.Attributes["id"].IsRequired() {
			t.Fatal("data source schema carries provider-only attributes")
		}
	}
}

func TestDatabaseErrorsCarryMessageAndExtras(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"time_not_restorable","message":"That time isn't restorable","restorableRanges":[{"from":"2026-11-01T00:00:00.000Z","to":"2026-11-02T00:00:00.000Z"}]}}`))
	})
	_, err := c.Do(context.Background(), service, "GET", "/v1/x", nil)
	var d diag.Diagnostics
	apiError(&d, err)
	if got := d.Errors()[0].Detail(); !strings.Contains(got, "That time isn't restorable") || !strings.Contains(got, `restorableRanges: [{"from":"2026-11-01T00:00:00.000Z"`) {
		t.Fatal(got)
	}
	// An access denial names only the permission, as everywhere in the provider.
	c = testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"access_denied","message":"Not allowed to databases.create; secret-token","x":1}}`))
	})
	_, err = c.Do(context.Background(), service, "GET", "/v1/x", nil)
	d = diag.Diagnostics{}
	apiError(&d, err)
	if got := d.Errors()[0].Detail(); !strings.Contains(got, "databases.create") || strings.Contains(got, "secret-token") || strings.Contains(got, "x:") {
		t.Fatal(got)
	}
}

// Users and logical databases keep no secret in state: nothing is sensitive, nothing holds a password.
func TestChildrenHoldNoPassword(t *testing.T) {
	for _, r := range []resource.Resource{&userResource{}, &logicalDatabaseResource{}} {
		for name, a := range schemaOf(r).Schema.Attributes {
			if strings.Contains(name, "password") || a.IsSensitive() {
				t.Fatalf("%T has %s", r, name)
			}
		}
	}
}

// A missing user plans status available, so the plan is an in-place update.
func TestMissingUserPlansAvailable(t *testing.T) {
	ctx := context.Background()
	r := &userResource{}
	m := userModel{LogicalDatabase: types.StringValue("shop"), Preset: types.StringValue("readWrite"), SecretID: types.StringValue("sec_222222222222")}
	m.ID, m.OrganizationID, m.ProjectID, m.DatabaseID, m.Name, m.Status = types.StringValue("dbi_123456789abc/app"), types.StringValue("org_test"), types.StringValue("prj_test"), types.StringValue("dbi_123456789abc"), types.StringValue("app"), types.StringValue("missing")
	s := asState(t, r, &m)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}, &resp)
	var status types.String
	resp.Plan.GetAttribute(ctx, path.Root("status"), &status)
	if resp.Diagnostics.HasError() || status.ValueString() != "available" {
		t.Fatalf("%v %v", resp.Diagnostics, status)
	}
	// A replacement creates it anew: status is left alone.
	resp = resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}, RequiresReplace: path.Paths{path.Root("name")}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}, &resp)
	resp.Plan.GetAttribute(ctx, path.Root("status"), &status)
	if status.ValueString() != "missing" {
		t.Fatalf("replace planned %v", status)
	}
}

// A read that fails after the create succeeded warns, so the user isn't tainted and replaced.
func TestUserCreateUnreadWarns(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"pending"}`))
		case strings.Contains(r.URL.Path, "operations"):
			_, _ = w.Write([]byte(`{"id":"op1","status":"succeeded"}`))
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
		}
	})
	r := &userResource{client: c}
	m := userModel{LogicalDatabase: types.StringValue("shop"), Preset: types.StringValue("readWrite"), SecretID: types.StringUnknown()}
	m.ID, m.OrganizationID, m.ProjectID, m.DatabaseID, m.Name, m.Status = types.StringUnknown(), types.StringUnknown(), types.StringValue("prj_test"), types.StringValue("dbi_123456789abc"), types.StringValue("app"), types.StringUnknown()
	p := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	var got userModel
	resp.State.Get(ctx, &got)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || got.Status.ValueString() != "available" || got.ID.ValueString() != "dbi_123456789abc/app" {
		t.Fatalf("%v %#v", resp.Diagnostics, got)
	}
}

// A create the API refuses (a name made in SQL) saves nothing, so there is nothing to destroy.
func TestRefusedUserCreateSavesNothing(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"already_exists","message":"A user named app exists"}}`))
	})
	r := &userResource{client: c}
	m := userModel{LogicalDatabase: types.StringValue("shop"), Preset: types.StringValue("readWrite"), SecretID: types.StringUnknown()}
	m.ID, m.OrganizationID, m.ProjectID, m.DatabaseID, m.Name, m.Status = types.StringUnknown(), types.StringUnknown(), types.StringValue("prj_test"), types.StringValue("dbi_123456789abc"), types.StringValue("app"), types.StringUnknown()
	p := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "A user named app exists") || !resp.State.Raw.IsNull() {
		t.Fatalf("%v %v", resp.Diagnostics, resp.State.Raw)
	}
}
