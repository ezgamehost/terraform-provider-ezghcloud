package iam

import (
	"context"
	"encoding/json"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateBotAndRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/organizations/org_test/bots" || r.Method != "POST" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "deploy" {
			t.Errorf("body %v", body)
		}
		_, _ = w.Write([]byte(`{"id":"bot-uuid","name":"deploy","slug":"bot_abc","resourceName":"ezgh:test:bot_abc"}`))
	}))
	defer server.Close()
	c := client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})
	d := definitions()["bot"]
	v := nullValues(d)
	v["name"] = types.StringValue("deploy")
	got, err := createEntity(context.Background(), c, d, v)
	if err != nil || stringValue(got, "id") != "bot-uuid" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestFrameworkCreatePersistsBotState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"bot-id","name":"deployer","slug":"bot_deployer","resourceName":"ezgh:test:bot_deployer"}`))
	}))
	defer server.Close()
	ctx := context.Background()
	d := definitions()["bot"]
	v := nullValues(d)
	v["name"] = types.StringValue("deployer")
	raw, err := objectValue(d, v).ToTerraformValue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := &iamResource{definition: d, client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})}
	result := resource.CreateResponse{State: tfsdk.State{Schema: schemaFor("bot")}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaFor("bot"), Raw: raw}}, &result)
	if result.Diagnostics.HasError() {
		t.Fatal(result.Diagnostics)
	}
	var id types.String
	result.Diagnostics.Append(result.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if result.Diagnostics.HasError() || id.ValueString() != "bot-id" {
		t.Fatalf("id=%v diag=%v", id, result.Diagnostics)
	}
}

func TestAcceptedInvitationDestroyNeverRemovesMember(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes++
		}
		_, _ = w.Write([]byte(`{"id":"invite-id","status":"accepted"}`))
	}))
	defer server.Close()
	c := client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})
	d := definitions()["invitation"]
	v := nullValues(d)
	v["id"] = types.StringValue("invite-id")
	if err := deleteEntity(context.Background(), c, d, v); err != nil || writes != 0 {
		t.Fatalf("writes=%d err=%v", writes, err)
	}
}

func TestMalformedEntityResponseDoesNotMeanAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"error":"incomplete"}`)) }))
	defer server.Close()
	c := client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})
	d := definitions()["bot"]
	v := nullValues(d)
	v["id"] = types.StringValue("bot-id")
	_, missing, err := readEntity(context.Background(), c, d, v)
	if err == nil || missing {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
}

func TestRelationshipChangesOnlyOneMemberWithETag(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Method != "GET" || r.URL.Path != "/v1/organizations/org_test/groups/group-a" {
				t.Errorf("%s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`{"id":"group-a","members":[{"type":"user","id":"other"}]}`))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/organizations/org_test/groups/group-a/members/bot-a" || r.Header.Get("If-Match") != `"v1"` {
			t.Errorf("unexpected %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"members":[{"type":"user","id":"other"},{"type":"bot","id":"bot-a"}]}`))
	}))
	defer server.Close()
	c := client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})
	d := definitions()["group_membership"]
	v := nullValues(d)
	v["group_id"] = types.StringValue("group-a")
	v["principal_id"] = types.StringValue("bot-a")
	v["principal_type"] = types.StringValue("bot")
	_, err := changeRelation(context.Background(), c, d, v, http.MethodPost)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestDeleteOfMissingEntitySucceeds(t *testing.T) {
	for _, orgStatus := range []int{http.StatusOK, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"gone"}}`))
				return
			}
			w.WriteHeader(orgStatus)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"gone"}}`))
		}))
		ctx := context.Background()
		d := definitions()["bot"]
		v := nullValues(d)
		v["id"] = types.StringValue("bot-id")
		v["organization_id"] = types.StringValue("org_test")
		raw, err := objectValue(d, v).ToTerraformValue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r := &iamResource{definition: d, client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"orgs": server.URL}})}
		resp := resource.DeleteResponse{}
		r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: schemaFor("bot"), Raw: raw}}, &resp)
		server.Close()
		if resp.Diagnostics.HasError() != (orgStatus == http.StatusNotFound) {
			t.Fatalf("org %d: diag=%v", orgStatus, resp.Diagnostics)
		}
	}
}
