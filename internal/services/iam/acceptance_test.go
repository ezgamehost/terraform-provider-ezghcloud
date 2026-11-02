package iam_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccIAMLifecycle(t *testing.T) {
	mu := sync.Mutex{}
	entities := map[string]map[string]any{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"version1"`)
		p := strings.TrimPrefix(r.URL.Path, "/v1/organizations/org_test")
		parts := strings.Split(strings.Trim(p, "/"), "/")
		if len(parts) == 1 && parts[0] == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "org_test"})
			return
		}
		kind := parts[0]
		id := kind + "-id"
		current := entities[kind]
		if len(parts) > 2 {
			if current == nil {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			key := "policyIds"
			var entry any = parts[len(parts)-1]
			if parts[2] == "members" {
				key = "members"
				entry = map[string]any{"type": "bot", "id": parts[len(parts)-1]}
			}
			if r.Header.Get("If-Match") != `"version1"` {
				t.Error("relationship omitted If-Match")
			}
			if r.Method == "POST" {
				current[key] = []any{entry}
			} else {
				current[key] = []any{}
			}
			_ = json.NewEncoder(w).Encode(current)
			return
		}
		switch r.Method {
		case "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			current = map[string]any{"id": id, "name": body["name"], "slug": "slug-" + kind, "resourceName": "ezgh:test:" + id, "description": "", "policyIds": []any{}, "members": []any{}}
			for k, v := range body {
				current[k] = v
			}
			entities[kind] = current
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(current)
		case "GET":
			if current == nil {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(current)
		case "PATCH":
			if r.Header.Get("If-Match") != `"version1"` {
				t.Error("update omitted If-Match")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k, v := range body {
				current[k] = v
			}
			_ = json.NewEncoder(w).Encode(current)
		case "DELETE":
			delete(entities, kind)
			w.WriteHeader(204)
		}
	}))
	defer s.Close()
	config := func(name string) string {
		return fmt.Sprintf(`provider "ezghcloud" {
 api_token="test"
 organization_id="org_test"
 endpoints={orgs=%q}
 }
 resource "ezghcloud_bot" "test" { name=%q }
 resource "ezghcloud_group" "test" { name="deployers" }
 resource "ezghcloud_policy" "test" {
 name="DeploymentReadAccess"
 statements=[{effect="allow",actions=["projects.get"],resources=["*"]}]
 }
 resource "ezghcloud_group_membership" "test" {
 group_id=ezghcloud_group.test.id
 principal_type="bot"
 principal_id=ezghcloud_bot.test.id
 }
 resource "ezghcloud_policy_attachment" "test" {
 principal_type="group"
 principal_id=ezghcloud_group.test.id
 policy_id=ezghcloud_policy.test.id
 }`, s.URL, name)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}, Steps: []resource.TestStep{
		{Config: config("deployer"), Check: resource.TestCheckResourceAttr("ezghcloud_bot.test", "name", "deployer")},
		{ResourceName: "ezghcloud_bot.test", ImportState: true, ImportStateId: "org_test/bots-id", ImportStateVerify: true},
		{ResourceName: "ezghcloud_group.test", ImportState: true, ImportStateId: "org_test/groups-id", ImportStateVerify: true},
		{ResourceName: "ezghcloud_policy.test", ImportState: true, ImportStateId: "org_test/policies-id", ImportStateVerify: true},
		{ResourceName: "ezghcloud_group_membership.test", ImportState: true, ImportStateId: "org_test/bot/bots-id/groups-id", ImportStateVerify: true},
		{ResourceName: "ezghcloud_policy_attachment.test", ImportState: true, ImportStateId: "org_test/group/groups-id/policies-id", ImportStateVerify: true},
		{Config: config("renamed"), Check: resource.TestCheckResourceAttr("ezghcloud_bot.test", "name", "renamed")},
	}, CheckDestroy: func(_ *terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if len(entities) != 0 {
			return fmt.Errorf("IAM entities remain: %v", entities)
		}
		return nil
	}})
}

func TestAccAPIKeySecretSurvivesRefresh(t *testing.T) {
	mu := sync.Mutex{}
	active := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		key := map[string]any{"id": "key-id", "name": "deployment", "prefix": "ezgh_test", "expiresAt": nil}
		switch {
		case strings.HasSuffix(r.URL.Path, "/permissions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": []string{"apiKeys.list"}})
		case r.Method == "POST":
			active = true
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"apiKey": key, "token": "one-time-sensitive-token"})
		case r.Method == "DELETE":
			active = false
			w.WriteHeader(204)
		case r.Method == "GET":
			rows := []any{}
			if active {
				rows = append(rows, key)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiKeys": rows, "nextCursor": nil})
		}
	}))
	defer s.Close()
	config := fmt.Sprintf(`provider "ezghcloud" {
 api_token="user-oauth-token"
 organization_id="org_test"
 endpoints={orgs=%q}
 }
 resource "ezghcloud_api_key" "test" {
 name="deployment"
 expires_in_seconds=3600
 }`, s.URL)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}, Steps: []resource.TestStep{
		{Config: config, Check: resource.TestCheckResourceAttr("ezghcloud_api_key.test", "token", "one-time-sensitive-token")},
		{RefreshState: true, Check: resource.TestCheckResourceAttr("ezghcloud_api_key.test", "token", "one-time-sensitive-token")},
		{ResourceName: "ezghcloud_api_key.test", ImportState: true, ImportStateId: "org_test/key-id", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"token", "expires_in_seconds"}},
	}, CheckDestroy: func(_ *terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if active {
			return fmt.Errorf("API key not revoked")
		}
		return nil
	}})
}
