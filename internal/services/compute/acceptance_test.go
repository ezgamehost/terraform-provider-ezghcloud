package compute_test

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
)

func TestAccComputeStorageLifecycle(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		last := parts[len(parts)-1]
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if strings.Contains(r.URL.Path, "/operations/") {
			send(map[string]any{"id": last, "status": "succeeded"})
			return
		}
		if last == "eni_123456789abc" {
			send(map[string]any{"slug": "eni_123456789abc", "subnet": "sbn_123456789abc", "attachment": map[string]any{"deleteOnTermination": true}})
			return
		}
		if last == "image-resolution" {
			send(map[string]any{"slug": "img_123456789abc", "name": "Ubuntu", "architecture": "amd64", "sha256": "abc", "minDiskBytes": "1073741824", "defaultUser": "ubuntu", "bootMode": "uefi"})
			return
		}
		if len(parts) == 5 && parts[3] == "projects" {
			send(map[string]any{"id": "project"})
			return
		}
		if r.Method == "POST" && last == "modify" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			obj := objects[parts[len(parts)-2]]
			for k, v := range body {
				obj[k] = v
			}
			w.Header().Set("Location", "/v1/organizations/org_test/operations/modify-volume")
			w.WriteHeader(202)
			send(map[string]any{"id": "modify-volume", "target": map[string]any{"id": obj["slug"]}, "volume": obj})
			return
		}
		if r.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			obj := body
			var id string
			switch last {
			case "ssh-keys":
				id = "ssh_123456789abc"
				obj["publicKey"] = strings.TrimSpace(obj["publicKey"].(string))
				obj["fingerprint"] = "SHA256:test"
			case "volumes":
				id = "vol_123456789abc"
				obj["status"] = "available"
				if _, ok := obj["iops"]; !ok {
					obj["iops"] = float64(1000)
				}
				if _, ok := obj["throughputMBps"]; !ok {
					obj["throughputMBps"] = float64(100)
				}
			case "instances":
				id = "ins_123456789abc"
				obj["status"] = "running"
				obj["rootVolumeId"] = "vol_aaaaaaaaaaaa"
				obj["volumeIds"] = []string{"vol_aaaaaaaaaaaa", "vol_123456789abc"}
				obj["networkInterfaceIds"] = []string{"eni_123456789abc"}
				obj["image"] = map[string]any{"id": body["image"], "alias": ""}
				obj["sshKeys"] = []map[string]any{{"id": "ssh_123456789abc"}}
				obj["bot"] = nil
			default:
				t.Errorf("unexpected POST %s", r.URL.Path)
				w.WriteHeader(500)
				return
			}
			obj["id"] = "uuid"
			obj["slug"] = id
			obj["name"] = body["name"]
			objects[id] = obj
			if last == "ssh-keys" {
				w.WriteHeader(201)
				send(obj)
				return
			}
			w.Header().Set("Location", "/v1/organizations/org_test/operations/op-"+id)
			w.WriteHeader(202)
			send(map[string]any{"id": "op-" + id, "target": map[string]any{"id": id}, "volume": obj})
			return
		}
		if obj, ok := objects[last]; ok {
			switch r.Method {
			case "GET":
				send(obj)
			case "PATCH":
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				for k, v := range body {
					obj[k] = v
				}
				send(obj)
			case "DELETE":
				delete(objects, last)
				if strings.HasPrefix(last, "ssh_") {
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Location", "/v1/organizations/org_test/operations/delete-"+last)
				w.WriteHeader(202)
				send(map[string]any{"id": "delete-" + last, "target": map[string]any{"id": last}})
			default:
				t.Errorf("unexpected method %s %s", r.Method, r.URL.Path)
				w.WriteHeader(500)
			}
			return
		}
		w.WriteHeader(404)
		send(map[string]any{"error": map[string]any{"code": "not_found", "message": "missing"}})
	}))
	defer server.Close()
	config := func(name string) string {
		size := "1073741824"
		if name == "second" {
			size = "2147483648"
		}
		return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, compute = %[1]q, blockstore = %[1]q, images = %[1]q, network = %[1]q }
}
resource "ezghcloud_ssh_key" "key" {
 project_id = "prj_test"
 name = %[2]q
 public_key = "ssh-ed25519 AAAA comment\n"
}
resource "ezghcloud_volume" "disk" {
 project_id = "prj_test"
 name = %[2]q
 volume_type = "gp1"
 size_bytes = %[3]q
}
resource "ezghcloud_instance" "server" {
 project_id = "prj_test"
 name = %[2]q
 instance_type = "t0.small"
 image = "linux/ubuntu:24.04"
 ssh_key_ids = [ezghcloud_ssh_key.key.id]
 volume_ids = [ezghcloud_volume.disk.id]
 network_interfaces = [{subnet_id = "sbn_123456789abc", firewall_ids = []}]
}
data "ezghcloud_volume" "disk" {
 project_id = "prj_test"
 id = ezghcloud_volume.disk.id
}
`, server.URL, name, size)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}, Steps: []resource.TestStep{
		{Config: config("first"), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("ezghcloud_instance.server", "id", "ins_123456789abc"), resource.TestCheckResourceAttr("ezghcloud_instance.server", "volume_ids.#", "1"), resource.TestCheckResourceAttr("data.ezghcloud_volume.disk", "iops", "1000"))},
		{Config: config("second"), Check: resource.TestCheckResourceAttr("ezghcloud_instance.server", "name", "second")},
		{ResourceName: "ezghcloud_ssh_key.key", ImportState: true, ImportStateId: "org_test/prj_test/ssh_123456789abc", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"public_key"}},
		{ResourceName: "ezghcloud_volume.disk", ImportState: true, ImportStateId: "org_test/prj_test/vol_123456789abc", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id"}},
		{ResourceName: "ezghcloud_instance.server", ImportState: true, ImportStateId: "org_test/prj_test/ins_123456789abc", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id", "image", "network_interfaces"}},
	}})
}

func TestAccInstanceDataSourceNullableFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "ins_123456789abc", "name": "store", "instanceType": "store.small", "status": "running", "rootVolumeId": nil, "bot": nil, "image": map[string]any{}, "networkInterfaceIds": []string{}, "volumeIds": []string{}, "tags": map[string]string{}})
	}))
	defer server.Close()
	config := fmt.Sprintf(`
 provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = {compute = %q}
 }
 data "ezghcloud_instance" "store" {
 project_id = "prj_test"
 id = "ins_123456789abc"
 }
 `, server.URL)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}, Steps: []resource.TestStep{{Config: config, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("data.ezghcloud_instance.store", "id", "ins_123456789abc"), resource.TestCheckNoResourceAttr("data.ezghcloud_instance.store", "root_volume_id"), resource.TestCheckNoResourceAttr("data.ezghcloud_instance.store", "bot_id"), resource.TestCheckNoResourceAttr("data.ezghcloud_instance.store", "image_id"))}}})
}
