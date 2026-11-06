package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func testClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return client.New(client.Config{OrganizationID: "org_test", Token: "test", Endpoints: map[string]string{"kubernetes": s.URL, "orgs": s.URL}})
}
func asState(t *testing.T, r resource.Resource, m any) tfsdk.State {
	t.Helper()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema}
	if d := s.Set(context.Background(), m); d.HasError() {
		t.Fatal(d)
	}
	return s
}

// activeCluster is a cluster as state holds it after create.
func activeCluster() clusterModel {
	ctx := context.Background()
	var m clusterModel
	m.fromAPI(ctx, clusterView{Slug: "kcl_123456789abc", Name: "prod", Status: "active", Version: "1.36", VPC: "vpc_123456789abc", Subnet: "sbn_123456789abc",
		Bot: "bot_123456789abc", ServiceIpv4Cidr: "10.100.0.0/16", ServiceIpv6Cidr: "fd10:100::/108"}, "org_test")
	m.ProjectID, m.BootstrapCreatorAccess, m.EncryptionKeyID = types.StringValue("prj_test"), types.BoolValue(true), types.StringNull()
	return m
}
func modifyCluster(t *testing.T, state, plan clusterModel) resource.ModifyPlanResponse {
	t.Helper()
	r := &clusterResource{}
	s, p := asState(t, r, &state), asState(t, r, &plan)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}}, &resp)
	return resp
}

func TestClusterCreateRetainsIdentityOnFailedOperation(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"queued","target":{"id":"uuid","resourceName":"ezgh:us-west-1:org_test:prj_test:kcl_123456789abc"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"op1","status":"failed","error":{"code":"network_gateway_required","message":"The subnet's route table sends no 0.0.0.0/0 to a Network Gateway"}}`))
	})
	r := &clusterResource{client: c}
	m := activeCluster()
	m.ID, m.OrganizationID, m.Status, m.OperationID, m.OperationTimeout = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringValue("1m")
	plan := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: plan.Schema, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "no 0.0.0.0/0") {
		t.Fatalf("failed operation not reported with its message: %v", resp.Diagnostics)
	}
	var got clusterModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueString() != "kcl_123456789abc" || got.OperationID.ValueString() != "op1" {
		t.Fatalf("identity lost: %#v", got)
	}
}

func TestClusterVersionSkipRefusedAtPlan(t *testing.T) {
	for _, to := range []string{"1.38", "1.35", "1.36.2"} {
		plan := activeCluster()
		plan.Version = types.StringValue(to)
		resp := modifyCluster(t, activeCluster(), plan)
		if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Upgrade one minor version at a time" {
			t.Fatalf("%s: %v", to, resp.Diagnostics)
		}
	}
	plan := activeCluster()
	plan.Version = types.StringValue("1.37")
	if resp := modifyCluster(t, activeCluster(), plan); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestClusterImmutableFieldsRefusedAtPlan(t *testing.T) {
	changes := map[string]func(*clusterModel){
		"subnet_id":                func(m *clusterModel) { m.SubnetID = types.StringValue("sbn_999999999999") },
		"bot_id":                   func(m *clusterModel) { m.BotID = types.StringUnknown() },
		"encryption_key_id":        func(m *clusterModel) { m.EncryptionKeyID = types.StringValue("kms_123456789abc") },
		"service_ipv4_cidr":        func(m *clusterModel) { m.ServiceIPv4CIDR = types.StringValue("10.200.0.0/16") },
		"bootstrap_creator_access": func(m *clusterModel) { m.BootstrapCreatorAccess = types.BoolValue(false) },
	}
	for name, change := range changes {
		plan := activeCluster()
		change(&plan)
		resp := modifyCluster(t, activeCluster(), plan)
		if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != name+" can't change" {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
	}
}

func TestClusterPrecheckFailedKeepsReasons(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"precheck_failed","message":"The upgrade can't start: Node group kng_000000000001 runs 1.33","reasons":[
			{"code":"node_group_skew","message":"Node group kng_000000000001 runs 1.33, more than 3 minor versions behind 1.37; upgrade it first"},
			{"code":"removed_api_in_use","message":"batch/v1beta1 cronjobs is removed in 1.37"}]}}`))
	})
	r := &clusterResource{client: c}
	old, m := activeCluster(), activeCluster()
	m.Version = types.StringValue("1.37")
	state, plan := asState(t, r, &old), asState(t, r, &m)
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: plan.Schema, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("refusal accepted")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"precheck_failed", "- Node group kng_000000000001 runs 1.33, more than 3 minor versions behind 1.37; upgrade it first (node_group_skew)", "- batch/v1beta1 cronjobs is removed in 1.37 (removed_api_in_use)"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("%q missing from:\n%s", want, detail)
		}
	}
}

func TestNodeGroupDesiredSizeNeverDrifts(t *testing.T) {
	ctx := context.Background()
	v := nodeGroupView{Slug: "kng_123456789abc", Cluster: "kcl_123456789abc", MinSize: 1, MaxSize: 10, DesiredSize: 7}
	m := nodeGroupModel{DesiredSize: types.Int64Value(2)}
	m.fromAPI(ctx, v, "org_test")
	if m.DesiredSize.ValueInt64() != 2 {
		t.Fatalf("Cluster Autoscaler's size read as drift: %v", m.DesiredSize)
	}
	imported := nodeGroupModel{DesiredSize: types.Int64Null()}
	imported.fromAPI(ctx, v, "org_test")
	if imported.DesiredSize.ValueInt64() != 7 {
		t.Fatalf("import didn't read the size: %v", imported.DesiredSize)
	}
}

func TestAddonConfigurationSemanticEquality(t *testing.T) {
	if !sameJSON(`{"a": 1, "b": [true]}`, `{"b":[true],"a":1}`) || sameJSON(`{"a":1}`, `{"a":2}`) {
		t.Fatal("sameJSON")
	}
	configured := `{ "replicaCount": 3 }`
	m := addonModel{Configuration: types.StringValue(configured)}
	m.fromAPI(addonView{Slug: "kad_123456789abc", Version: "1.12.0", Configuration: json.RawMessage(`{"replicaCount":3}`)}, "org_test")
	if m.Configuration.ValueString() != configured {
		t.Fatalf("formatting read as drift: %v", m.Configuration)
	}
	m.fromAPI(addonView{Slug: "kad_123456789abc", Version: "1.12.0", Configuration: json.RawMessage(`{"replicaCount":4}`)}, "org_test")
	if m.Configuration.ValueString() != `{"replicaCount":4}` {
		t.Fatalf("a real change was hidden: %v", m.Configuration)
	}
	m.fromAPI(addonView{Slug: "kad_123456789abc", Version: "1.12.0", Configuration: json.RawMessage(`null`)}, "org_test")
	if !m.Configuration.IsNull() || !m.Version.IsNull() {
		t.Fatalf("none configured, unpinned: %v %v", m.Configuration, m.Version)
	}
	m.fromAPI(addonView{Slug: "kad_123456789abc", Version: "1.12.0", Pinned: true}, "org_test")
	if m.Version.ValueString() != "1.12.0" || m.InstalledVersion.ValueString() != "1.12.0" {
		t.Fatalf("pinned: %v", m.Version)
	}
	if b := (addonModel{Version: types.StringNull(), Configuration: types.StringNull()}).body(); b["pinned"] != false || b["configuration"] != nil {
		t.Fatalf("following the cluster: %v", b)
	}
}

func TestAccessEntryPolicyScope(t *testing.T) {
	ctx := context.Background()
	policies, _ := types.ListValueFrom(ctx, accessPolicyType, []accessPolicy{
		{Policy: types.StringValue("view"), Namespaces: types.ListNull(types.StringType)},
		{Policy: types.StringValue("admin"), Namespaces: listValue(ctx, []string{})},
		{Policy: types.StringValue("edit"), Namespaces: listValue(ctx, []string{"shop", "web"})},
	})
	body, _ := json.Marshal(policiesBody(ctx, policies, &diag.Diagnostics{}))
	if want := `[{"namespaces":null,"policy":"view"},{"namespaces":null,"policy":"admin"},{"namespaces":["shop","web"],"policy":"edit"}]`; string(body) != want {
		t.Fatalf("got %s", body)
	}
	m := accessEntryModel{AccessPolicies: policies}
	m.fromAPI(ctx, accessEntryView{Slug: "kae_123456789abc", AccessPolicies: []struct {
		Policy     string
		Namespaces []string
	}{{"view", nil}, {"admin", nil}, {"edit", []string{"shop", "web"}}}}, "org_test")
	if !m.AccessPolicies.Equal(policies) {
		t.Fatalf("cluster-wide written as [] drifted: %v", m.AccessPolicies)
	}
}

// A create whose wait the timeout cuts short keeps the cluster in state, creating, as a warning; the
// next Read waits for the operation and reads the cluster as it ended.
func TestClusterCutShortWaitKeepsStateAndReadWaits(t *testing.T) {
	ctx := context.Background()
	var done atomic.Bool
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","status":"queued","target":{"id":"uuid","resourceName":"ezgh:us-west-1:org_test:prj_test:kcl_123456789abc"}}`))
		case strings.Contains(r.URL.Path, "/operations/"):
			status := "running"
			if done.Load() {
				status = "succeeded"
			}
			_, _ = w.Write([]byte(`{"id":"op1","status":"` + status + `"}`))
		default:
			status := "creating"
			if done.Load() {
				status = "active"
			}
			_, _ = w.Write([]byte(`{"slug":"kcl_123456789abc","status":"` + status + `","version":"1.36","endpoint":{"publicAccessCidrs":[]},"logging":{"types":[]}}`))
		}
	})
	r := &clusterResource{client: c}
	m := activeCluster()
	m.ID, m.OrganizationID, m.Status, m.OperationID, m.OperationTimeout = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringValue("1s")
	plan := asState(t, r, &m)
	created := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: plan.Schema, Raw: plan.Raw}}, &created)
	var got clusterModel
	created.State.Get(ctx, &got)
	if created.Diagnostics.HasError() || created.Diagnostics.WarningsCount() != 1 || got.ID.ValueString() != "kcl_123456789abc" || got.Status.ValueString() != "creating" || got.OperationID.ValueString() != "op1" {
		t.Fatalf("cut-short create: %v %#v", created.Diagnostics, got)
	}
	done.Store(true)
	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	read.State.Get(ctx, &got)
	if read.Diagnostics.HasError() || got.Status.ValueString() != "active" {
		t.Fatalf("read didn't wait: %v %v", read.Diagnostics, got.Status)
	}
}

// A node group updating under an operation state holds is waited for, then read again.
func TestNodeGroupReadWaitsWhileUpdating(t *testing.T) {
	ctx := context.Background()
	var waited atomic.Bool
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/op2") {
			waited.Store(true)
			_, _ = w.Write([]byte(`{"id":"op2","status":"succeeded"}`))
			return
		}
		status := "updating"
		if waited.Load() {
			status = "active"
		}
		_, _ = w.Write([]byte(`{"slug":"kng_123456789abc","cluster":"kcl_123456789abc","status":"` + status + `","version":"1.37"}`))
	})
	r := &nodeGroupResource{client: c}
	var m nodeGroupModel
	m.fromAPI(ctx, nodeGroupView{Slug: "kng_123456789abc", Cluster: "kcl_123456789abc", Status: "updating", Version: "1.36", DesiredSize: 1}, "org_test")
	m.ProjectID, m.OperationID = types.StringValue("prj_test"), types.StringValue("op2")
	state := asState(t, r, &m)
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	var got nodeGroupModel
	read.State.Get(ctx, &got)
	if read.Diagnostics.HasError() || !waited.Load() || got.Status.ValueString() != "active" || got.Version.ValueString() != "1.37" {
		t.Fatalf("%v waited=%v %v", read.Diagnostics, waited.Load(), got.Status)
	}
}

// A failed node group is planned as a replacement.
func TestNodeGroupFailedIsReplaced(t *testing.T) {
	ctx := context.Background()
	r := &nodeGroupResource{}
	var m nodeGroupModel
	m.fromAPI(ctx, nodeGroupView{Slug: "kng_123456789abc", Cluster: "kcl_123456789abc", Status: "failed"}, "org_test")
	m.ProjectID = types.StringValue("prj_test")
	s := asState(t, r, &m)
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}, &resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 1 || resp.RequiresReplace[0].String() != "status" {
		t.Fatalf("%v %v", resp.Diagnostics, resp.RequiresReplace)
	}
}

// An imported cluster has no bootstrap_creator_access (the API doesn't show it): whatever the config
// says is kept, even false, without a can't-change refusal.
func TestClusterImportedCreatorAccess(t *testing.T) {
	var m clusterModel
	m.fromAPI(context.Background(), clusterView{Slug: "kcl_123456789abc"}, "org_test")
	if !m.BootstrapCreatorAccess.IsNull() {
		t.Fatalf("import guessed %v", m.BootstrapCreatorAccess)
	}
	state, plan := activeCluster(), activeCluster()
	state.BootstrapCreatorAccess, plan.BootstrapCreatorAccess = types.BoolNull(), types.BoolValue(false)
	if resp := modifyCluster(t, state, plan); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

// A failed add-on is replaced, except one that can't be removed: that stays, with a warning.
func TestAddonFailedReplacedUnlessLasting(t *testing.T) {
	ctx := context.Background()
	r := &addonResource{}
	for name, replaced := range map[string]bool{"metrics-server": true, "pod-identity-agent": true, "coredns": false} {
		var m addonModel
		m.fromAPI(addonView{Slug: "kad_123456789abc", Cluster: "kcl_123456789abc", Name: name, Status: "failed", Version: "1.0.0"}, "org_test")
		m.ProjectID = types.StringValue("prj_test")
		s := asState(t, r, &m)
		resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: s, Plan: tfsdk.Plan{Schema: s.Schema, Raw: s.Raw}}, &resp)
		if resp.Diagnostics.HasError() || (len(resp.RequiresReplace) == 1) != replaced || (resp.Diagnostics.WarningsCount() == 1) == replaced {
			t.Fatalf("%s: %v %v", name, resp.Diagnostics, resp.RequiresReplace)
		}
	}
}

// An add-on a new cluster doesn't install keeps its addon_exists refusal: it was made elsewhere.
func TestAddonExistsOutsideDefaultsIsRefused(t *testing.T) {
	ctx := context.Background()
	var listed atomic.Bool
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			listed.Store(true)
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"addon_exists","message":"The cluster already has metrics-server"}}`))
	})
	r := &addonResource{client: c}
	m := addonModel{ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), ClusterID: types.StringValue("kcl_123456789abc"),
		ResourceName: types.StringUnknown(), Name: types.StringValue("metrics-server"), Version: types.StringNull(), InstalledVersion: types.StringUnknown(),
		Configuration: types.StringNull(), Status: types.StringUnknown(), OperationID: types.StringUnknown(), OperationTimeout: types.StringValue("1m")}
	plan := asState(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: plan.Schema, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "addon_exists") || listed.Load() {
		t.Fatalf("listed=%v %v", listed.Load(), resp.Diagnostics)
	}
}
