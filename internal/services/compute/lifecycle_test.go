package compute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func resourcePlan(t *testing.T, r resource.Resource, model any) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema}
	if d := s.Set(ctx, model); d.HasError() {
		t.Fatal(d)
	}
	return tfsdk.Plan{Schema: sr.Schema, Raw: s.Raw}
}
func testClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return client.New(client.Config{OrganizationID: "org_test", Token: "test", Endpoints: map[string]string{"compute": s.URL, "blockstore": s.URL, "images": s.URL, "orgs": s.URL, "network": s.URL}})
}
func TestVolumeTimeoutRetainsIdentity(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","target":{"id":"vol_123456789abc"},"volume":{"slug":"vol_123456789abc"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"op1","status":"failed","error":{"code":"failure","message":"failed"}}`))
	})
	r := &volumeResource{client: c}
	m := volumeModel{ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("disk"), VolumeType: types.StringValue("gp1"), SizeBytes: types.StringValue("1073741824"), IOPS: types.Int64Unknown(), ThroughputMBps: types.Int64Unknown(), Tags: mapValue(ctx, nil), DeletionProtection: types.BoolValue(false), Status: types.StringUnknown(), OperationID: types.StringUnknown(), OperationTimeout: types.StringValue("1s")}
	plan := resourcePlan(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed operation was accepted")
	}
	var got volumeModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueString() != "vol_123456789abc" || got.OperationID.ValueString() != "op1" {
		t.Fatalf("identity lost: %#v", got)
	}
}
func TestInstanceLaunchPinsImageAndPreservesIndependentVolumes(t *testing.T) {
	ctx := context.Background()
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Query().Get("ref") != "":
			_, _ = w.Write([]byte(`{"id":"uuid","slug":"img_123456789abc"}`))
		case r.Method == "POST":
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op1")
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"op1","target":{"id":"ins_123456789abc"}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"op1","status":"failed","error":{"code":"failure","message":"failed"}}`))
		}
	})
	r := &instanceResource{client: c}
	ifs, _ := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: interfaceTypes}, []interfaceModel{{Subnet: types.StringValue("sbn_123456789abc"), Existing: types.StringNull(), PrivateIPv4: types.StringNull(), IPv4: types.BoolNull(), IPv6: types.BoolNull(), Firewalls: types.ListValueMust(types.StringType, nil)}})
	m := instanceModel{ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("server"), InstanceType: types.StringValue("t0.small"), Image: types.StringValue("linux/ubuntu:24.04"), ResolvedImageID: types.StringUnknown(), BotID: types.StringNull(), SSHKeyIDs: stringsValue(ctx, nil), UserData: types.StringNull(), RootDevice: types.StringValue("volume"), RootVolume: types.ObjectNull(rootVolumeTypes), Volumes: stringsValue(ctx, []string{"vol_123456789abc"}), Interfaces: ifs, Tags: mapValue(ctx, nil), Status: types.StringUnknown(), RootVolumeID: types.StringUnknown(), InterfaceIDs: types.ListUnknown(types.StringType), OperationID: types.StringUnknown(), OperationTimeout: types.StringValue("1s")}
	plan := resourcePlan(t, r, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed waiter accepted")
	}
	var got instanceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueString() != "ins_123456789abc" {
		t.Fatal("instance identity lost")
	}
	if body["image"] != "img_123456789abc" {
		t.Fatalf("unresolved image sent: %v", body)
	}
	vols := body["volumes"].([]any)
	v := vols[0].(map[string]any)
	if len(v) != 1 || v["volumeId"] != "vol_123456789abc" {
		t.Fatalf("volume ownership altered: %v", v)
	}
	ints := body["networkInterfaces"].([]any)
	fw, exists := ints[0].(map[string]any)["firewallIds"]
	if !exists || len(fw.([]any)) != 0 {
		t.Fatal("explicit empty firewalls lost")
	}
}
func TestRead404DoesNotForgetHiddenProject(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
	})
	missing, err := confirmedMissing(ctx, c, "prj_test", &client.Error{StatusCode: 404})
	if missing || err == nil {
		t.Fatal("hidden project interpreted as deleted resource")
	}
}
func TestVolumeGrowthRejectsShrink(t *testing.T) {
	if validateGrowth("1073741824", "1073741823") == nil {
		t.Fatal("shrink allowed")
	}
	if err := validateGrowth("1073741824", "9223372036854775808"); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceReadDoesNotAdoptRootVolume(t *testing.T) {
	ctx := context.Background()
	root := "vol_aaaaaaaaaaaa"
	m := instanceModel{}
	m.fromAPI(ctx, instanceView{Slug: "ins_123456789abc", RootVolumeID: &root, VolumeIDs: []string{root, "vol_bbbbbbbbbbbb"}}, "org_test")
	var ids []string
	if d := m.Volumes.ElementsAs(ctx, &ids, false); d.HasError() {
		t.Fatal(d)
	}
	if len(ids) != 1 || ids[0] != "vol_bbbbbbbbbbbb" {
		t.Fatalf("root volume adopted as independent volume: %v", ids)
	}
}
func TestSSHKeyReadPreservesConfiguredFormatting(t *testing.T) {
	ctx := context.Background()
	original := "ssh-ed25519 AAAA comment\n"
	m := sshKeyModel{PublicKey: types.StringValue(original)}
	m.fromAPI(ctx, sshKeyView{Slug: "ssh_123456789abc", PublicKey: "ssh-ed25519 AAAA comment"}, "org_test")
	if m.PublicKey.ValueString() != original {
		t.Fatal("API normalization creates inconsistent apply state")
	}
}

func TestRefreshResumesPendingVolumeOperation(t *testing.T) {
	ctx := context.Background()
	reads, operations := 0, 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") {
			operations++
			_, _ = w.Write([]byte(`{"id":"op1","status":"succeeded"}`))
			return
		}
		reads++
		status := "creating"
		if reads > 1 {
			status = "available"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "uuid", "slug": "vol_123456789abc", "name": "disk", "volumeType": "gp1", "sizeBytes": "1073741824", "iops": 1000, "throughputMBps": 100, "deletionProtection": false, "tags": map[string]string{}, "status": status})
	})
	r := &volumeResource{client: c}
	m := volumeModel{ID: types.StringValue("vol_123456789abc"), OrganizationID: types.StringValue("org_test"), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("disk"), VolumeType: types.StringValue("gp1"), SizeBytes: types.StringValue("1073741824"), IOPS: types.Int64Value(1000), ThroughputMBps: types.Int64Value(100), Tags: mapValue(ctx, nil), DeletionProtection: types.BoolValue(false), Status: types.StringValue("creating"), OperationID: types.StringValue("op1"), OperationTimeout: types.StringValue("1s")}
	p := resourcePlan(t, r, &m)
	state := tfsdk.State{Schema: p.Schema, Raw: p.Raw}
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got volumeModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if operations != 1 || got.Status.ValueString() != "available" {
		t.Fatalf("pending create was not reconciled: operations=%d status=%s", operations, got.Status.ValueString())
	}
}

func TestRefreshAfterOutOfBandDeleteWarns(t *testing.T) {
	ctx := context.Background()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"id":"op1","status":"succeeded"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "uuid", "slug": "vol_123456789abc", "name": "disk", "volumeType": "gp1", "sizeBytes": "1073741824", "iops": 1000, "throughputMBps": 100, "deletionProtection": false, "tags": map[string]string{}, "status": "deleting"})
	})
	r := &volumeResource{client: c}
	m := volumeModel{ID: types.StringValue("vol_123456789abc"), OrganizationID: types.StringValue("org_test"), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("disk"), VolumeType: types.StringValue("gp1"), SizeBytes: types.StringValue("1073741824"), IOPS: types.Int64Value(1000), ThroughputMBps: types.Int64Value(100), Tags: mapValue(ctx, nil), DeletionProtection: types.BoolValue(false), Status: types.StringValue("creating"), OperationID: types.StringValue("op1"), OperationTimeout: types.StringValue("1s")}
	p := resourcePlan(t, r, &m)
	state := tfsdk.State{Schema: p.Schema, Raw: p.Raw}
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	var got volumeModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if resp.Diagnostics.HasError() || got.Status.ValueString() != "deleting" {
		t.Fatalf("diag=%v status=%s", resp.Diagnostics, got.Status.ValueString())
	}
}

// deleteWaitsOutServer answers DELETE 409, then GET with status for two reads, then 404; the project always answers.
func deleteWaitsOutServer(status string, deletes, gets *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			*deletes++
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"operation_in_progress","message":"busy"}}`))
		case strings.HasSuffix(r.URL.Path, "/projects/prj_test"):
			_, _ = w.Write([]byte(`{}`))
		default:
			*gets++
			if *gets > 2 {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"gone"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
		}
	}
}

func TestInstanceDeleteWaitsOutTerminating(t *testing.T) {
	deletePoll = time.Millisecond
	ctx := context.Background()
	deletes, gets := 0, 0
	r := &instanceResource{client: testClient(t, deleteWaitsOutServer("terminating", &deletes, &gets))}
	m := instanceModel{}
	m.fromAPI(ctx, instanceView{Slug: "ins_123456789abc"}, "org_test")
	m.RootVolume, m.Interfaces = types.ObjectNull(rootVolumeTypes), types.ListNull(types.ObjectType{AttrTypes: interfaceTypes})
	m.ProjectID, m.OperationTimeout = types.StringValue("prj_test"), types.StringValue("5s")
	state := tfsdk.State{Schema: resourcePlan(t, r, &m).Schema, Raw: resourcePlan(t, r, &m).Raw}
	resp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || deletes != 1 || gets != 3 {
		t.Fatalf("diag=%v deletes=%d gets=%d", resp.Diagnostics, deletes, gets)
	}
}

func TestVolumeDeleteWaitsOutDeleting(t *testing.T) {
	deletePoll = time.Millisecond
	ctx := context.Background()
	deletes, gets := 0, 0
	r := &volumeResource{client: testClient(t, deleteWaitsOutServer("deleting", &deletes, &gets))}
	m := volumeModel{ID: types.StringValue("vol_123456789abc"), OrganizationID: types.StringValue("org_test"), ProjectID: types.StringValue("prj_test"), Tags: mapValue(ctx, nil), DeletionProtection: types.BoolValue(false), OperationTimeout: types.StringValue("5s")}
	state := tfsdk.State{Schema: resourcePlan(t, r, &m).Schema, Raw: resourcePlan(t, r, &m).Raw}
	resp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || deletes != 1 || gets != 3 {
		t.Fatalf("diag=%v deletes=%d gets=%d", resp.Diagnostics, deletes, gets)
	}
}

func TestVolumeDeleteBusyOtherStatusKeeps409(t *testing.T) {
	deletePoll = time.Millisecond
	ctx := context.Background()
	deletes, gets := 0, 0
	r := &volumeResource{client: testClient(t, deleteWaitsOutServer("available", &deletes, &gets))}
	m := volumeModel{ID: types.StringValue("vol_123456789abc"), OrganizationID: types.StringValue("org_test"), ProjectID: types.StringValue("prj_test"), Tags: mapValue(ctx, nil), DeletionProtection: types.BoolValue(false), OperationTimeout: types.StringValue("5s")}
	state := tfsdk.State{Schema: resourcePlan(t, r, &m).Schema, Raw: resourcePlan(t, r, &m).Raw}
	resp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || gets != 1 {
		t.Fatalf("diag=%v gets=%d", resp.Diagnostics, gets)
	}
}
