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

// mainTable is the fake VPC's main route table: what a subnet without a table of its own reports.
const mainTable = "rtb_mmmmmmmmmmmm"

// fakeRoutes is a route table's routes as network-api shows them: the VPC's local routes first,
// then the written ones with their state. A NAT gateway route is shown blackhole, as it is until
// the gateway is active: the provider must not see that as drift.
func fakeRoutes(written any) []any {
	routes := []any{map[string]any{"destination": "10.40.0.0/16", "target": map[string]any{"type": "local", "id": nil}, "state": "active"},
		map[string]any{"destination": "fd00::/56", "target": map[string]any{"type": "local", "id": nil}, "state": "active"}}
	list, _ := written.([]any)
	for _, r := range list {
		route := r.(map[string]any)
		route["state"] = "active"
		if route["target"].(map[string]any)["type"] == "natGateway" {
			route["state"] = "blackhole"
		}
		routes = append(routes, route)
	}
	return routes
}

func TestAccNetworkLifecycle(t *testing.T) {
	// The NAT gateway is its own operation, as network-api serves it: a create or delete answers
	// 202 with the gateway and its own URL as Location; natReads counts the reads since, the
	// first answering it provisioning or deleting, the next active or 404.
	natReads := 0
	var mu sync.Mutex
	// objects and versions are keyed by ID. A kind's first object gets ids[kind]; the private
	// subnet (10.40.2.0/24) is the one kind with a second.
	objects := map[string]map[string]any{}
	versions := map[string]int{}
	ids := map[string]string{"vpcs": "vpc_aaaaaaaaaaaa", "subnets": "sbn_aaaaaaaaaaaa", "firewalls": "fwl_aaaaaaaaaaaa", "nat-gateways": "nat_aaaaaaaaaaaa", "route-tables": "rtb_aaaaaaaaaaaa"}
	const privateSubnet = "sbn_bbbbbbbbbbbb"
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
		kind, key := parts[5], ids[parts[5]]
		if len(parts) > 6 {
			key = parts[6]
		}
		var current map[string]any
		if len(parts) > 6 {
			current = objects[key]
		}
		if kind == "nat-gateways" && current != nil && r.Method == "GET" {
			natReads++
			// The first read after a create or delete fails in a way the client doesn't retry:
			// the wait must outlast it, not fail (and taint) the gateway.
			if natReads == 1 && (current["status"] == "provisioning" || current["status"] == "deleting") {
				w.WriteHeader(500)
				w.Write([]byte(`{"error":{"code":"internal_error","message":"try again"}}`))
				return
			}
			if natReads > 2 && current["status"] == "provisioning" {
				current["status"] = "active"
			}
			if natReads > 2 && current["status"] == "deleting" {
				delete(objects, key)
				current = nil
			}
		}
		if kind == "nat-gateways" && (r.Method == "POST" || r.Method == "DELETE") {
			natReads = 0
			if r.Method == "POST" {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if r.Header.Get("Idempotency-Key") == "" || len(body) != 3 {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":{"code":"invalid_request"}}`))
					return
				}
				current = map[string]any{"id": "resource-uuid", "slug": ids[kind], "name": body["name"], "resourceName": "ezgh:test:" + ids[kind], "organizationId": "org_test", "projectId": "prj_test", "region": "test",
					"vpc": body["vpc"], "subnet": body["subnet"], "publicIpv4Addresses": []map[string]any{{"id": "pip_aaaaaaaaaaaa", "address": "203.0.113.7"}}, "createdAt": "2026-11-10T00:00:00.000Z", "updatedAt": "2026-11-10T00:00:00.000Z"}
				objects[key] = current
			}
			if current == nil {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			current["status"] = map[string]string{"POST": "provisioning", "DELETE": "deleting"}[r.Method]
			w.Header().Set("Location", "/v1/organizations/org_test/projects/prj_test/nat-gateways/resource-uuid")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(current)
			return
		}
		if r.Method == "GET" && current == nil {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		if r.Method == "DELETE" {
			for _, o := range objects {
				if o["routeTable"] == key {
					w.WriteHeader(409)
					w.Write([]byte(`{"error":{"code":"route_table_in_use","message":"a subnet uses the route table"}}`))
					return
				}
			}
			delete(objects, key)
			w.WriteHeader(204)
			return
		}
		if r.Method == "POST" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["ipv4Cidr"] == "10.40.2.0/24" {
				key = privateSubnet
			}
			current = map[string]any{"id": "resource-uuid", "slug": key, "name": body["name"], "description": body["description"], "tags": body["tags"], "status": "available", "resourceName": "ezgh:test:" + key, "organizationId": "org_test", "projectId": "prj_test"}
			if kind == "route-tables" {
				delete(current, "status")
				current["vpc"], current["main"], current["subnets"], current["routes"] = body["vpc"], false, []string{}, fakeRoutes(body["routes"])
			} else if kind == "firewalls" {
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
					current["routeTable"] = mainTable
				} else {
					current["defaultFirewall"] = "fwl_bbbbbbbbbbbb"
				}
			}
			objects[key] = current
			versions[key]++
		}
		if r.Method == "PATCH" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			for field, value := range body {
				current[field] = value
			}
			// null is the VPC's main table.
			if table, ok := body["routeTable"]; ok && table == nil {
				current["routeTable"] = mainTable
			}
			// A route table's ETag is its routes' version: a rename doesn't move it.
			if kind != "route-tables" {
				versions[key]++
			}
		}
		if r.Method == "PUT" {
			if r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, versions[key]) {
				w.WriteHeader(412)
				w.Write([]byte(`{"error":{"code":"precondition_failed"}}`))
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if kind == "route-tables" {
				current["routes"] = fakeRoutes(body["routes"])
			} else {
				current["rules"] = body["rules"]
			}
			versions[key]++
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, versions[key]))
		if r.Method == "POST" {
			w.WriteHeader(201)
		}
		if r.Method == "PUT" && kind == "firewalls" {
			json.NewEncoder(w).Encode(map[string]any{"firewall": current, "ruleIds": []string{}})
			return
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer s.Close()
	// association is the private subnet's route_table_id line, or a bare dependency in its place:
	// a subnet that stops naming its table stays on it.
	const associated, omitted = "route_table_id = ezghcloud_route_table.test.id", "depends_on = [ezghcloud_route_table.test]"
	config := func(name string, publicIPv4 bool, port int, association, moreRoutes string) string {
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
resource "ezghcloud_nat_gateway" "test" {
 project_id = "prj_test"
 name = %[2]q
 vpc_id = ezghcloud_vpc.test.id
 subnet_id = ezghcloud_subnet.test.id
 timeouts = { create = "5m" }
}
resource "ezghcloud_route_table" "test" {
 project_id = "prj_test"
 vpc_id = ezghcloud_vpc.test.id
 name = %[2]q
 routes = [{destination="0.0.0.0/0",target_type="natGateway",target_id=ezghcloud_nat_gateway.test.id}%[6]s]
}
resource "ezghcloud_subnet" "private" {
 project_id = "prj_test"
 vpc_id = ezghcloud_vpc.test.id
 name = "private"
 ipv4_cidr = "10.40.2.0/24"
 %[5]s
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
`, s.URL, name, publicIPv4, port, association, moreRoutes)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(cloudprovider.New("test")())},
		Steps: []resource.TestStep{
			{Config: config("first", true, 22, associated, ""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_route_table.test", "id", ids["route-tables"]), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "main", "false"), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "routes.#", "1"),
				resource.TestCheckResourceAttr("ezghcloud_route_table.test", "routes.0.target_id", ids["nat-gateways"]), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "etag", `"1"`),
				resource.TestCheckResourceAttr("ezghcloud_subnet.private", "route_table_id", ids["route-tables"]), resource.TestCheckResourceAttr("ezghcloud_subnet.test", "route_table_id", mainTable), resource.TestCheckResourceAttr("ezghcloud_vpc.test", "id", ids["vpcs"]), resource.TestCheckResourceAttr("data.ezghcloud_subnet.test", "vpc_id", ids["vpcs"]), resource.TestCheckResourceAttr("data.ezghcloud_firewall.test", "rules.#", "1"),
				resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "id", ids["nat-gateways"]), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "status", "active"), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "resource_name", "ezgh:test:"+ids["nat-gateways"]),
				resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "subnet_id", ids["subnets"]), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "public_ipv4_addresses.#", "1"),
				resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "public_ipv4_addresses.0.id", "pip_aaaaaaaaaaaa"), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "public_ipv4_addresses.0.address", "203.0.113.7"))},
			{ResourceName: "ezghcloud_nat_gateway.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["nat-gateways"], ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "ezghcloud_route_table.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["route-tables"], ImportStateVerify: true},
			{ResourceName: "ezghcloud_subnet.private", ImportState: true, ImportStateId: "org_test/prj_test/" + privateSubnet, ImportStateVerify: true},
			{ResourceName: "ezghcloud_vpc.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["vpcs"], ImportStateVerify: true},
			{ResourceName: "ezghcloud_subnet.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["subnets"], ImportStateVerify: true},
			{ResourceName: "ezghcloud_firewall.test", ImportState: true, ImportStateId: "org_test/prj_test/" + ids["firewalls"], ImportStateVerify: true},
			{Config: config("renamed", false, 443, omitted, `,{destination="192.168.0.0/16",target_type="networkInterface",target_id="eni_aaaaaaaaaaaa"}`), Check: resource.ComposeAggregateTestCheckFunc(
				// Renamed and its routes replaced in one apply; the private subnet no longer names the table and stays on it.
				resource.TestCheckResourceAttr("ezghcloud_route_table.test", "name", "renamed"), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "routes.#", "2"), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "etag", `"2"`),
				resource.TestCheckResourceAttr("ezghcloud_subnet.private", "route_table_id", ids["route-tables"]), resource.TestCheckResourceAttr("ezghcloud_vpc.test", "name", "renamed"), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "name", "renamed"), resource.TestCheckResourceAttr("ezghcloud_nat_gateway.test", "id", ids["nat-gateways"]), resource.TestCheckResourceAttr("ezghcloud_subnet.test", "auto_assign_public_ipv4", "false"), resource.TestCheckResourceAttr("ezghcloud_firewall.test", "rules.0.ports_from", "443"))},
			// Back to the main table by naming it.
			{Config: config("renamed", false, 443, "route_table_id = ezghcloud_subnet.test.route_table_id\n depends_on = [ezghcloud_route_table.test]", ""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_subnet.private", "route_table_id", mainTable), resource.TestCheckResourceAttr("ezghcloud_route_table.test", "routes.#", "1"))},
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
