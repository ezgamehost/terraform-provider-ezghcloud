package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccProjectLifecycle(t *testing.T) {
	var current map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/organizations/org_test" {
			json.NewEncoder(w).Encode(map[string]any{"id": "org_test"})
			return
		}
		switch r.Method {
		case "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			current = map[string]any{"id": "project-uuid", "slug": "prj_aaaaaaaaaaaa", "organizationId": "org_test", "name": body["name"], "folderId": nil}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(current)
		case "GET":
			if current == nil {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			json.NewEncoder(w).Encode(current)
		case "PATCH":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			current["name"] = body["name"]
			current["folderId"] = body["folderId"]
			json.NewEncoder(w).Encode(current)
		case "DELETE":
			current = nil
			w.WriteHeader(204)
		}
	}))
	defer s.Close()
	config := func(name string) string {
		return fmt.Sprintf(`provider "ezghcloud" {
 api_token = "test"
 organization_id = "org_test"
 endpoints = { orgs = %q }
 }
 resource "ezghcloud_project" "test" { name = %q }
 `, s.URL, name)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: config("first"), Check: resource.TestCheckResourceAttr("ezghcloud_project.test", "name", "first")},
		{ResourceName: "ezghcloud_project.test", ImportState: true, ImportStateId: "org_test/project-uuid", ImportStateVerify: true},
		{Config: config("renamed"), Check: resource.TestCheckResourceAttr("ezghcloud_project.test", "name", "renamed")},
	}, CheckDestroy: func(_ *terraform.State) error {
		if current != nil {
			return fmt.Errorf("project still exists")
		}
		return nil
	}})
}
