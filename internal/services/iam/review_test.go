package iam

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

func TestDeletedRelationParentConfirmedByOrganization(t *testing.T) {
	for _, orgVisible := range []bool{true, false} {
		t.Run(map[bool]string{true: "visible", false: "hidden"}[orgVisible], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/organizations/org_test" && orgVisible {
					_, _ = w.Write([]byte(`{"id":"org_test"}`))
					return
				}
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
			}))
			defer server.Close()
			c := client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})
			d := definitions()["group_membership"]
			v := nullValues(d)
			v["group_id"] = types.StringValue("deleted")
			v["principal_type"] = types.StringValue("bot")
			v["principal_id"] = types.StringValue("bot-id")
			exists, err := readRelation(context.Background(), c, d, v)
			if exists || ((err == nil) != orgVisible) {
				t.Errorf("read exists=%v err=%v", exists, err)
			}
			_, err = changeRelation(context.Background(), c, d, v, http.MethodDelete)
			if (err == nil) != orgVisible {
				t.Errorf("delete err=%v", err)
			}
		})
	}
}

func TestOrganizationMismatchBlocksUpdateAndDelete(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	ctx := context.Background()
	d := definitions()["bot"]
	v := nullValues(d)
	v["id"] = types.StringValue("bot-id")
	v["organization_id"] = types.StringValue("different-org")
	v["name"] = types.StringValue("name")
	raw, err := objectValue(d, v).ToTerraformValue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := &iamResource{definition: d, client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})}
	update := resource.UpdateResponse{State: tfsdk.State{Schema: schemaFor("bot")}}
	r.Update(ctx, resource.UpdateRequest{State: tfsdk.State{Schema: schemaFor("bot"), Raw: raw}, Plan: tfsdk.Plan{Schema: schemaFor("bot"), Raw: raw}}, &update)
	deletion := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: schemaFor("bot"), Raw: raw}}, &deletion)
	if calls != 0 || !update.Diagnostics.HasError() || !deletion.Diagnostics.HasError() {
		t.Fatalf("calls=%d update=%v delete=%v", calls, update.Diagnostics, deletion.Diagnostics)
	}
}
