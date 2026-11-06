package kubernetes_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestLiveKubernetesLifecycle makes one cluster and one gs-1 node group of one node on real EZGH,
// and destroys only those. It runs only with TF_ACC=1 and EZGH_PROFILE=dev, which CI never sets:
// the provider has no profiles, so this is the explicit opt-in. Not run before Gate 2.
func TestLiveKubernetesLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	t.Setenv("EZGHCLOUD_PREVIEW_KUBERNETES", "1")
	env := map[string]string{}
	for _, k := range []string{"EZGHCLOUD_API_TOKEN", "EZGHCLOUD_ORGANIZATION_ID", "EZGHCLOUD_TEST_PROJECT_ID", "EZGHCLOUD_TEST_SUBNET_ID", "EZGHCLOUD_TEST_CLUSTER_BOT_ID", "EZGHCLOUD_TEST_NODE_BOT_ID"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Fatalf("%s is required", k)
		}
	}
	out, err := exec.Command("ezgh", "--profile", "dev", "orgs", "list", "-o", "json").Output()
	if err != nil || !strings.Contains(string(out), `"`+env["EZGHCLOUD_ORGANIZATION_ID"]+`"`) {
		t.Fatalf("the dev profile doesn't list organization %s: %v", env["EZGHCLOUD_ORGANIZATION_ID"], err)
	}
	config := fmt.Sprintf(`
resource "ezghcloud_kubernetes_cluster" "live" {
 project_id = %[1]q
 name = "tf-live-kubernetes"
 subnet_id = %[2]q
 bot_id = %[3]q
}
resource "ezghcloud_kubernetes_node_group" "live" {
 project_id = %[1]q
 cluster_id = ezghcloud_kubernetes_cluster.live.id
 name = "tf-live-nodes"
 instance_type = "gs-1.large"
 subnet_id = %[2]q
 bot_id = %[4]q
 min_size = 1
 max_size = 1
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_SUBNET_ID"], env["EZGHCLOUD_TEST_CLUSTER_BOT_ID"], env["EZGHCLOUD_TEST_NODE_BOT_ID"])
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("ezghcloud_kubernetes_cluster.live", "status", "active"),
			resource.TestCheckResourceAttrSet("ezghcloud_kubernetes_cluster.live", "certificate_authority"),
			resource.TestCheckResourceAttr("ezghcloud_kubernetes_node_group.live", "status", "active"))},
	}})
}
