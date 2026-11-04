package files_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestLiveFilesLifecycle creates a file system, a mount target and an access point on real EZGH and
// destroys them. It runs only with TF_ACC=1 and EZGH_LIVE=1, which CI never sets: the explicit
// opt-in. Point it at a test organization, never production customer projects.
func TestLiveFilesLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_LIVE") != "1" {
		t.Skip("set TF_ACC=1 and EZGH_LIVE=1 to run against real EZGH")
	}
	env := map[string]string{}
	for _, k := range []string{"EZGHCLOUD_API_TOKEN", "EZGHCLOUD_ORGANIZATION_ID", "EZGHCLOUD_TEST_PROJECT_ID", "EZGHCLOUD_TEST_VPC_ID", "EZGHCLOUD_TEST_SUBNET_ID"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Fatalf("%s is required", k)
		}
	}
	config := fmt.Sprintf(`
resource "ezghcloud_file_system" "live" {
 project_id = %[1]q
 name = "tf-live-files"
 vpc_id = %[2]q
}
resource "ezghcloud_mount_target" "live" {
 project_id = %[1]q
 file_system_id = ezghcloud_file_system.live.id
 subnet_id = %[3]q
}
resource "ezghcloud_access_point" "live" {
 project_id = %[1]q
 file_system_id = ezghcloud_file_system.live.id
 name = "tf-live-wp-content"
 root_directory = {
  path = "/wordpress/wp-content"
  creation_info = { owner_uid = 33, owner_gid = 33, permissions = "755" }
 }
 posix_user = { uid = 33, gid = 33 }
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_VPC_ID"], env["EZGHCLOUD_TEST_SUBNET_ID"])
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("ezghcloud_file_system.live", "status", "available"),
			resource.TestCheckResourceAttr("ezghcloud_mount_target.live", "status", "available"),
			resource.TestCheckResourceAttrSet("ezghcloud_mount_target.live", "hostname"),
			resource.TestCheckResourceAttrSet("ezghcloud_mount_target.live", "ip_address"),
			resource.TestCheckResourceAttr("ezghcloud_access_point.live", "status", "available"))},
	}})
}
