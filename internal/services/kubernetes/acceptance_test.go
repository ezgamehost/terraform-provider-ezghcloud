package kubernetes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	framework "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// fixture is kubernetes-api over memory: clusters, node groups and add-ons answer 202 and their
// operations succeed at once; access entries and pod identity associations are synchronous.
type fixture struct {
	mu      sync.Mutex
	objects map[string]map[string]any // by slug
	next    int
	calls   []string
	*httptest.Server
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{objects: map[string]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

var prefixes = map[string]string{"clusters": "kcl_", "node-groups": "kng_", "addons": "kad_", "access-entries": "kae_", "pod-identity-associations": "kpa_"}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	fail := func(status int, code string) {
		send(status, map[string]any{"error": map[string]any{"code": code, "message": code}})
	}
	accepted := func(slug string) {
		w.Header().Set("Location", "/v1/organizations/org_test/operations/op-"+slug)
		send(202, map[string]any{"id": "op-" + slug, "status": "queued", "target": map[string]any{"id": "uuid", "resourceName": "ezgh:us-west-1:org_test:prj_test:" + slug}})
	}
	// /v1/organizations/org_test/projects/prj_test/clusters/kcl_…/node-groups/kng_…
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(parts) == 5 && parts[3] == "operations" {
		send(200, map[string]any{"id": parts[4], "status": "succeeded"})
		return
	}
	if len(parts) == 5 && parts[3] == "projects" {
		send(200, map[string]any{"id": "prj_test"})
		return
	}
	if len(parts) < 6 || parts[5] != "clusters" {
		fail(404, "not_found")
		return
	}
	collection, cluster := "clusters", ""
	rest := parts[6:]
	if len(rest) >= 2 {
		cluster, collection, rest = rest[0], rest[1], rest[2:]
		if f.objects[cluster] == nil || prefixes[collection] == "" {
			fail(404, "not_found")
			return
		}
	}
	call := r.Method + " " + collection
	if len(rest) == 1 {
		call += "/" + rest[0]
	}
	f.calls = append(f.calls, call)
	async := collection == "clusters" || collection == "node-groups" || collection == "addons"
	if len(rest) == 0 && r.Method == "GET" {
		items := []any{}
		for _, o := range f.objects {
			if o["cluster"] == cluster && strings.HasPrefix(fmt.Sprint(o["slug"]), prefixes[collection]) {
				items = append(items, o)
			}
		}
		send(200, map[string]any{"addons": items, "nextCursor": nil})
		return
	}
	if len(rest) == 0 && r.Method == "POST" {
		if collection == "addons" {
			for _, o := range f.objects {
				if o["cluster"] == cluster && o["name"] == body["name"] {
					fail(409, "addon_exists")
					return
				}
			}
		}
		f.next++
		slug := fmt.Sprintf("%s%012d", prefixes[collection], f.next)
		obj := map[string]any{"id": "uuid", "slug": slug, "resourceName": "ezgh:us-west-1:org_test:prj_test:" + slug, "status": "active", "tags": map[string]any{}}
		if cluster != "" {
			obj["cluster"] = cluster
		}
		switch collection {
		case "clusters":
			obj["version"], obj["patchVersion"], obj["vpc"], obj["serviceIpv4Cidr"], obj["serviceIpv6Cidr"] = "1.36", "1.36.4", "vpc_123456789abc", "10.100.0.0/16", "fd10:100::/108"
			obj["endpoint"] = map[string]any{"hostname": slug + ".k8s.us-west-1.ezghcompute.com", "privateHostname": slug + ".private.k8s.us-west-1.ezghcompute.com",
				"publicIpv4": "203.0.113.7", "privateAddresses": []any{}, "publicAccessCidrs": []any{"0.0.0.0/0"}}
			obj["certificateAuthority"], obj["encryptionKey"], obj["nodeFirewall"] = "-----BEGIN CERTIFICATE-----\n", nil, "fwl_123456789abc"
			obj["oidcIssuer"] = "https://kubernetes-api.ezghcloud.com/oidc/" + slug
			obj["logging"] = map[string]any{"types": []any{}, "obsProject": nil}
			obj["maintenanceWindow"] = map[string]any{"day": "sunday", "startHour": 9}
		case "node-groups":
			obj["version"], obj["rootVolumeSizeGiB"], obj["labels"], obj["taints"], obj["sshKeyIds"] = "1.36", 20, map[string]any{}, []any{}, []any{}
			obj["updateConfig"], obj["autoscalingGroup"], obj["desiredSize"] = map[string]any{"maxUnavailablePercent": 25}, "asg_123456789abc", body["minSize"]
		case "addons":
			obj["version"], obj["pinned"], obj["configuration"] = "1.12.0", false, nil
		case "access-entries":
			obj["username"] = "ezgh:" + fmt.Sprint(body["principal"].(map[string]any)["id"])
		}
		for k, v := range body {
			switch k {
			case "subnet", "bot", "instanceType", "minSize", "maxSize", "desiredSize", "name", "labels", "taints", "sshKeyIds", "tags", "principal", "kubernetesGroups", "accessPolicies",
				"namespace", "serviceAccount", "version", "pinned", "configuration", "updateConfig", "logging", "maintenanceWindow":
				obj[k] = v
			case "publicAccessCidrs":
				obj["endpoint"].(map[string]any)["publicAccessCidrs"] = v
			}
		}
		f.objects[slug] = obj
		if async {
			accepted(slug)
		} else {
			send(201, obj)
		}
		return
	}
	obj := f.objects[rest[len(rest)-1]]
	if len(rest) != 1 || obj == nil {
		fail(404, "not_found")
		return
	}
	switch r.Method {
	case "GET":
		send(200, obj)
	case "PATCH":
		for k, v := range body {
			if k == "publicAccessCidrs" {
				obj["endpoint"].(map[string]any)["publicAccessCidrs"] = v
			} else {
				obj[k] = v
			}
		}
		if async {
			accepted(rest[0])
		} else {
			send(200, obj)
		}
	case "DELETE":
		if collection == "clusters" {
			for _, o := range f.objects {
				if o["cluster"] == rest[0] && strings.HasPrefix(fmt.Sprint(o["slug"]), "kng_") {
					fail(409, "cluster_has_node_groups")
					return
				}
			}
		}
		if obj["name"] == "coredns" {
			fail(409, "addon_not_removable")
			return
		}
		delete(f.objects, rest[0])
		if collection == "clusters" { // its add-ons and the rest go with it
			for k, o := range f.objects {
				if o["cluster"] == rest[0] {
					delete(f.objects, k)
				}
			}
		}
		if async {
			accepted(rest[0])
		} else {
			w.WriteHeader(204)
		}
	}
}

func (f *fixture) provider() string {
	return fmt.Sprintf(`
provider "ezghcloud" {
 api_token = "fixture"
 organization_id = "org_test"
 endpoints = { orgs = %[1]q, kubernetes = %[1]q }
}
`, f.URL)
}

func (f *fixture) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func action(name string, a plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, a)}}
}

const stack = `
resource "ezghcloud_kubernetes_cluster" "prod" {
 project_id = "prj_test"
 name = "prod"
 version = %[1]q
 subnet_id = "sbn_123456789abc"
 bot_id = "bot_cluster00001"
 public_access_cidrs = [%[2]s]
 tags = { team = "web" }
}
resource "ezghcloud_kubernetes_node_group" "general" {
 project_id = "prj_test"
 cluster_id = ezghcloud_kubernetes_cluster.prod.id
 name = "general"
 instance_type = "gp-1.large"
 subnet_id = "sbn_123456789abc"
 bot_id = "bot_nodes0000001"
 min_size = 1
 max_size = %[3]d
 desired_size = 2
 taints = [{ key = "dedicated", value = "web", effect = "NoSchedule" }]
}
resource "ezghcloud_kubernetes_addon" "metrics" {
 project_id = "prj_test"
 cluster_id = ezghcloud_kubernetes_cluster.prod.id
 name = "metrics-server"
 configuration = %[4]q
}
resource "ezghcloud_kubernetes_addon" "coredns" {
 project_id = "prj_test"
 cluster_id = ezghcloud_kubernetes_cluster.prod.id
 name = "coredns"
 version = "1.12.0"
}
resource "ezghcloud_kubernetes_access_entry" "ops" {
 project_id = "prj_test"
 cluster_id = ezghcloud_kubernetes_cluster.prod.id
 principal_type = "group"
 principal_id = "grp_123456789abc"
 kubernetes_groups = [%[5]s]
 access_policy {
  policy = "view"
 }
 access_policy {
  policy = "edit"
  namespaces = ["shop"]
 }
}
resource "ezghcloud_kubernetes_pod_identity_association" "checkout" {
 project_id = "prj_test"
 cluster_id = ezghcloud_kubernetes_cluster.prod.id
 namespace = "shop"
 service_account = "checkout"
 bot_id = %[6]q
}
data "ezghcloud_kubernetes_cluster" "prod" {
 project_id = "prj_test"
 id = ezghcloud_kubernetes_cluster.prod.id
}
`

func TestAccKubernetesLifecycle(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_KUBERNETES", "1")
	f := newFixture(t)
	// The cluster comes with coredns, as every cluster does; the coredns resource takes it over.
	config := func(version, cidrs string, max int, cfg, groups, bot string) string {
		return f.provider() + fmt.Sprintf(stack, version, cidrs, max, cfg, groups, bot)
	}
	before := config("1.36", `"0.0.0.0/0"`, 3, `{"replicas": 1}`, `"ops"`, "bot_checkout0001")
	after := config("1.37", `"203.0.113.0/24"`, 5, `{ "replicas" : 2 }`, `"ops", "audit"`, "bot_checkout0002")
	c, ng := "ezghcloud_kubernetes_cluster.prod", "ezghcloud_kubernetes_node_group.general"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if len(f.objects) != 0 {
				return fmt.Errorf("left %v", f.objects)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{PreConfig: func() {
				// What cluster create installs: coredns, following the cluster.
				f.objects["kad_000000000099"] = map[string]any{"id": "uuid", "slug": "kad_000000000099", "resourceName": "r", "name": "coredns", "status": "active",
					"version": "1.11.0", "pinned": false, "configuration": nil, "cluster": "kcl_000000000001"}
			}, Config: before, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(c, "id", "kcl_000000000001"),
				resource.TestCheckResourceAttr(c, "endpoint_hostname", "kcl_000000000001.k8s.us-west-1.ezghcompute.com"),
				resource.TestCheckResourceAttr(c, "bootstrap_creator_access", "true"),
				resource.TestCheckResourceAttr(c, "maintenance_window_day", "sunday"),
				resource.TestCheckResourceAttr("data.ezghcloud_kubernetes_cluster.prod", "certificate_authority", "-----BEGIN CERTIFICATE-----\n"),
				resource.TestCheckResourceAttr(ng, "desired_size", "2"),
				resource.TestCheckResourceAttr(ng, "autoscaling_group_id", "asg_123456789abc"),
				resource.TestCheckResourceAttr(ng, "taints.0.effect", "NoSchedule"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_addon.coredns", "id", "kad_000000000099"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_addon.coredns", "installed_version", "1.12.0"),
				resource.TestCheckNoResourceAttr("ezghcloud_kubernetes_addon.metrics", "version"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_access_entry.ops", "username", "ezgh:grp_123456789abc"),
				resource.TestCheckNoResourceAttr("ezghcloud_kubernetes_access_entry.ops", "access_policy.0.namespaces"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_access_entry.ops", "access_policy.1.namespaces.0", "shop"))},
			// Cluster Autoscaler moves the size: no drift.
			{PreConfig: func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				for slug, o := range f.objects {
					if strings.HasPrefix(slug, "kng_") {
						o["desiredSize"] = 3
					}
				}
			}, Config: before, ConfigPlanChecks: action(ng, plancheck.ResourceActionNoop)},
			{Config: after, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(c, "version", "1.37"),
				resource.TestCheckResourceAttr(c, "public_access_cidrs.0", "203.0.113.0/24"),
				resource.TestCheckResourceAttr(ng, "max_size", "5"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_access_entry.ops", "kubernetes_groups.1", "audit"),
				resource.TestCheckResourceAttr("ezghcloud_kubernetes_pod_identity_association.checkout", "bot_id", "bot_checkout0002"),
				func(*terraform.State) error {
					if n := f.count("PATCH clusters/"); n != 1 {
						return fmt.Errorf("%d cluster PATCHes", n)
					}
					return nil
				})},
			{Config: strings.Replace(after, `policy = "edit"`, `policy = "clusterAdmin"`, 1), ExpectError: regexp.MustCompile(`clusterAdmin is for the whole cluster`)},
			{Config: strings.Replace(after, `version = "1.37"`, `version = "1.36"`, 1), ExpectError: regexp.MustCompile(`Upgrade one minor version at a time`)},
			{Config: strings.Replace(after, `bot_id = "bot_cluster00001"`, `bot_id = "bot_cluster00002"`, 1), ExpectError: regexp.MustCompile(`bot_id can't change`)},
			{ResourceName: c, ImportState: true, ImportStateId: "org_test/prj_test/kcl_000000000001", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id", "bootstrap_creator_access"}},
			{ResourceName: ng, ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				return "org_test/prj_test/kcl_000000000001/" + s.RootModule().Resources[ng].Primary.ID, nil
			}, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id", "desired_size"}},
			{ResourceName: "ezghcloud_kubernetes_addon.metrics", ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				return "org_test/prj_test/kcl_000000000001/" + s.RootModule().Resources["ezghcloud_kubernetes_addon.metrics"].Primary.ID, nil
			}, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id", "configuration"}},
			{ResourceName: "ezghcloud_kubernetes_addon.coredns", ImportState: true, ImportStateId: "org_test/prj_test/kcl_000000000001/kad_000000000099", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"operation_id"}},
			{ResourceName: "ezghcloud_kubernetes_pod_identity_association.checkout", ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				return "org_test/prj_test/kcl_000000000001/" + s.RootModule().Resources["ezghcloud_kubernetes_pod_identity_association.checkout"].Primary.ID, nil
			}, ImportStateVerify: true},
			{ResourceName: "ezghcloud_kubernetes_access_entry.ops", ImportState: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				return "org_test/prj_test/kcl_000000000001/" + s.RootModule().Resources["ezghcloud_kubernetes_access_entry.ops"].Primary.ID, nil
			}, ImportStateVerify: true},
		}})
}

// Without EZGHCLOUD_PREVIEW_KUBERNETES=1 the provider serves no Kubernetes resource or data
// source, and its endpoints description doesn't name the service.
func TestKubernetesHiddenWithoutPreview(t *testing.T) {
	ctx := context.Background()
	for _, preview := range []string{"", "1"} {
		t.Setenv("EZGHCLOUD_PREVIEW_KUBERNETES", preview)
		p := provider.New("test")()
		var names []string
		for _, r := range p.Resources(ctx) {
			var resp fwresource.MetadataResponse
			r().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "ezghcloud"}, &resp)
			names = append(names, resp.TypeName)
		}
		for _, d := range p.DataSources(ctx) {
			var resp datasource.MetadataResponse
			d().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "ezghcloud"}, &resp)
			names = append(names, resp.TypeName)
		}
		var schema framework.SchemaResponse
		p.Schema(ctx, framework.SchemaRequest{}, &schema)
		described := strings.Contains(schema.Schema.Attributes["endpoints"].GetDescription(), "kubernetes")
		served := strings.Contains(strings.Join(names, " "), "kubernetes")
		if served != (preview == "1") || described != (preview == "1") {
			t.Fatalf("preview %q: served %v, described %v: %v", preview, served, described, names)
		}
	}
}

// An endpoints.kubernetes override is refused without the preview switch, as an unknown service.
func TestKubernetesEndpointNeedsPreview(t *testing.T) {
	ctx := context.Background()
	for _, preview := range []string{"", "1"} {
		t.Setenv("EZGHCLOUD_PREVIEW_KUBERNETES", preview)
		p := provider.New("test")()
		var sr framework.SchemaResponse
		p.Schema(ctx, framework.SchemaRequest{}, &sr)
		raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
			"api_token":       tftypes.NewValue(tftypes.String, "token"),
			"organization_id": tftypes.NewValue(tftypes.String, "org_test"),
			"endpoints":       tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{"kubernetes": tftypes.NewValue(tftypes.String, "https://kubernetes.test")}),
		})
		var resp framework.ConfigureResponse
		p.Configure(ctx, framework.ConfigureRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}, &resp)
		if resp.Diagnostics.HasError() != (preview == "") {
			t.Fatalf("preview %q: %v", preview, resp.Diagnostics)
		}
	}
}
