package network_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cloudprovider "github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccNetworkLifecycle(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	versions := map[string]int{}
	ids := map[string]string{"vpcs": "vpc_aaaaaaaaaaaa", "subnets": "sbn_aaaaaaaaaaaa", "firewalls": "fwl_aaaaaaaaaaaa"}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 5 {
			json.NewEncoder(w).Encode(map[string]any{"id": "prj_test"})
			return
		}
		if len(parts) < 6 {
			w.WriteHeader(404)
			return
		}
		kind := parts[5]
		current := objects[kind]
		if r.Method == "GET" && current == nil {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		if r.Method == "DELETE" {
			delete(objects, kind)
			w.WriteHeader(204)
			return
		}
		if r.Method == "POST" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			current = map[string]any{"id": "resource-uuid", "slug": ids[kind], "name": body["name"], "description": body["description"], "tags": body["tags"], "status": "available", "resourceName": "ezgh:test:" + ids[kind], "organizationId": "org_test", "projectId": "prj_test"}
			if kind == "firewalls" {
				current["status"] = "active"
				current["rules"] = body["rules"]
				if current["description"] == nil {
					current["description"] = ""
				}
			} else {
				current["ipv4Cidr"] = body["ipv4Cidr"]
				current["ipv6Cidr"] = "fd00::/56"
				if kind == "subnets" {
					current["ipv6Cidr"] = "fd00:0:0:1::/64"
					current["vpc"] = body["vpc"]
					current["autoAssignPublicIpv4"] = false
					current["autoAssignPublicIpv6"] = false
				} else {
					current["defaultFirewall"] = "fwl_bbbbbbbbbbbb"
				}
			}
			objects[kind] = current
			versions[kind]++
		}
		if r.Method == "PATCH" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			for key, value := range body {
				current[key] = value
			}
			versions[kind]++
		}
		if r.Method == "PUT" {
			if r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, versions[kind]) {
				w.WriteHeader(412)
				w.Write([]byte(`{"error":{"code":"precondition_failed"}}`))
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			current["rules"] = body["rules"]
			versions[kind]++
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, versions[kind]))
		if r.Method == "POST" {
			w.WriteHeader(201)
		}
		if r.Method == "PUT" {
			json.NewEncoder(w).Encode(map[string]any{"firewall": current, "ruleIds": []string{}})
			return
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer s.Close()
	config := func(name string, publicIPv4 bool, port int) string {
		return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "test"
 organization_id = "org_test"
 endpoints = { network = %[1]q, firewall = %[1]q, orgs = %[1]q }
}
resource "ezghcloud_vpc" "test" {
 project_id = "prj_test"
 name = %[2]q
 ipv4_cidr = "10.40.0.0/16"
 tags = { purpose = "terraform-test" }
}
resource "ezghcloud_subnet" "test" {
 project_id = "prj_test"
 vpc_id = ezghcloud_vpc.test.id
 name = %[2]q
 ipv4_cidr = "10.40.1.0/24"
 auto_assign_public_ipv4 = %[3]t
}
resource "ezghcloud_firewall" "test" {
 project_id = "prj_test"
 name = %[2]q
 rules = [{direction="inbound",protocol="tcp",peer_cidr="10.40.0.0/16",ports_from=%[4]d,ports_to=%[4]d}]
}
data "ezghcloud_vpc" "test" {
 project_id = "prj_test"
 id = ezghcloud_vpc.test.id
}
data "ezghcloud_subnet" "test" {
 project_id = "prj_test"
 id = ezghcloud_subnet.test.id
}
data "ezghcloud_firewall" "test" {
 project_id = "prj_test"
 id = ezghcloud_firewall.test.id
}
`, s.URL, name, publicIPv4, port)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(cloudprovider.New("test")())},
		Steps: []resource.TestStep{
			{Config: config("first", true, 22), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("ezghcloud_vpc.test", "id", ids["vpcs"]), resource.TestCheckResourceAttr("data.ezghcloud_subnet.test", "vpc_id", ids["vpcs"]), resource.TestCheckResourceAttr("data.ezghcloud_firewall.test", "rules.#", "1"))},
			{ResourceName: "ezghcloud_vpc.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["vpcs"], ImportStateVerify: true},
			{ResourceName: "ezghcloud_subnet.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["subnets"], ImportStateVerify: true},
			{ResourceName: "ezghcloud_firewall.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["firewalls"], ImportStateVerify: true},
			{Config: config("renamed", false, 443), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("ezghcloud_vpc.test", "name", "renamed"), resource.TestCheckResourceAttr("ezghcloud_subnet.test", "auto_assign_public_ipv4", "false"), resource.TestCheckResourceAttr("ezghcloud_firewall.test", "rules.0.ports_from", "443"))},
		}, CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if len(objects) != 0 {
				return fmt.Errorf("network resources remain: %v", objects)
			}
			return nil
		},
	})
}
