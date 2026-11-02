package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProjectUpdateRejectsOtherOrganizationBeforeRequest(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{}`)) }))
	defer s.Close()
	r := &projectResource{c: client.New(client.Config{OrganizationID: "org_new", Endpoints: map[string]string{"orgs": s.URL}})}
	ctx := context.Background()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	m := projectModel{ID: types.StringValue("id"), Slug: types.StringValue("prj_test"), Name: types.StringValue("changed"), Organization: types.StringValue("org_old"), Folder: types.StringNull()}
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: state.Raw}, State: state}, &resp)
	if calls != 0 || !resp.Diagnostics.HasError() {
		t.Fatalf("calls=%d diagnostics=%v", calls, resp.Diagnostics)
	}
}
