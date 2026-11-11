package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Routes are replaced only against the version the plan was made from, local routes and route
// states never reach state, and a response without the table's identity can't replace it.
func TestRouteTableReplacesRoutesConditionally(t *testing.T) {
	ctx := context.Background()
	etag, replaced, malformed := `"1"`, 0, false
	local := json.RawMessage(`{"destination":"10.0.0.0/16","target":{"type":"local","id":null},"state":"active"}`)
	routes := []json.RawMessage{local}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if malformed {
			w.Write([]byte(`{}`))
			return
		}
		var body struct {
			Routes []json.RawMessage `json:"routes"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch r.Method {
		case "POST":
			routes = append([]json.RawMessage{local}, body.Routes...)
			w.Header().Set("ETag", etag)
			w.WriteHeader(201)
		case "PATCH":
			t.Error("metadata patched without a metadata change")
		case "PUT":
			if r.Header.Get("If-Match") != etag {
				w.WriteHeader(412)
				w.Write([]byte(`{"error":{"code":"precondition_failed","message":"routes changed"}}`))
				return
			}
			routes = append([]json.RawMessage{local}, body.Routes...)
			replaced++
			etag = `"2"`
		}
		w.Header().Set("ETag", etag)
		json.NewEncoder(w).Encode(map[string]any{"slug": "rtb_abcdefghijkl", "name": "private", "description": nil, "tags": map[string]string{}, "resourceName": "ezgh:test", "vpc": "vpc_abcdefghijkl", "main": false, "routes": routes})
	}))
	defer server.Close()
	r := &routeTableResource{client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"network": server.URL}})}
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	routeSet := func(destination string) types.Set {
		set, _ := types.SetValueFrom(ctx, routeType, []routeModel{{types.StringValue(destination), types.StringValue("natGateway"), types.StringValue("nat_abcdefghijkl")}})
		return set
	}
	m := routeTableModel{ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("private"), Description: types.StringNull(), Tags: types.MapValueMust(types.StringType, map[string]attr.Value{}),
		ResourceName: types.StringUnknown(), VPCID: types.StringValue("vpc_abcdefghijkl"), Main: types.BoolUnknown(), ETag: types.StringUnknown(), Routes: routeSet("0.0.0.0/0")}
	plan := tfsdk.Plan{Schema: s.Schema}
	if diags := plan.Set(ctx, m); diags.HasError() {
		t.Fatal(diags)
	}
	created := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	var got routeTableModel
	created.State.Get(ctx, &got)
	if !got.Routes.Equal(m.Routes) || got.ETag.ValueString() != `"1"` || got.ID.ValueString() != "rtb_abcdefghijkl" {
		t.Fatalf("created %s %s %s", got.ID, got.ETag, got.Routes)
	}
	got.Routes = routeSet("192.0.2.0/24")
	if diags := plan.Set(ctx, got); diags.HasError() {
		t.Fatal(diags)
	}
	updated := resource.UpdateResponse{State: created.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State}, &updated)
	if updated.Diagnostics.HasError() || replaced != 1 {
		t.Fatalf("replace: %d %v", replaced, updated.Diagnostics)
	}
	// The same plan against the state it was made from is now stale: routes written since must survive.
	refused := resource.UpdateResponse{State: created.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State}, &refused)
	if !refused.Diagnostics.HasError() || replaced != 1 {
		t.Fatal("concurrent routes were overwritten")
	}
	malformed = true
	read := resource.ReadResponse{State: updated.State}
	r.Read(ctx, resource.ReadRequest{State: updated.State}, &read)
	read.State.Get(ctx, &got)
	if !read.Diagnostics.HasError() || got.ID.ValueString() != "rtb_abcdefghijkl" {
		t.Fatal("malformed API response accepted")
	}
}

// A destination the API would spell differently, or give two routes, is refused at plan time.
func TestRouteTableValidatesDestinations(t *testing.T) {
	ctx := context.Background()
	r := &routeTableResource{}
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	for destination, bad := range map[string]bool{"0.0.0.0/0": false, "2001:db8::/32": false, "10.0.1.7/24": true, "2001:DB8::/32": true, "10.0.1.0": true} {
		set, _ := types.SetValueFrom(ctx, routeType, []routeModel{{types.StringValue(destination), types.StringValue("natGateway"), types.StringValue("nat_abcdefghijkl")}})
		config := tfsdk.Config{Schema: s.Schema}
		state := tfsdk.State{Schema: s.Schema}
		state.Set(ctx, routeTableModel{ProjectID: types.StringValue("prj_test"), Name: types.StringValue("private"), Tags: types.MapNull(types.StringType), VPCID: types.StringValue("vpc_abcdefghijkl"), Routes: set})
		config.Raw = state.Raw
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: config}, &resp)
		if resp.Diagnostics.HasError() != bad {
			t.Errorf("%s: %v", destination, resp.Diagnostics)
		}
	}
}
