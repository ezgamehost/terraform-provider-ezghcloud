package databases_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestLiveDatabaseLifecycle creates one gs-1.large database on real EZGH and destroys it, with no final
// backup. It runs only with TF_ACC=1 and EZGH_PROFILE=dev, which CI never sets: the provider has no
// profiles, so this is the explicit opt-in.
func TestLiveDatabaseLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	env := map[string]string{}
	for _, k := range []string{"EZGHCLOUD_API_TOKEN", "EZGHCLOUD_ORGANIZATION_ID", "EZGHCLOUD_TEST_PROJECT_ID", "EZGHCLOUD_TEST_SUBNET_ID"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Fatalf("%s is required", k)
		}
	}
	out, err := exec.Command("ezgh", "--profile", "dev", "orgs", "list", "-o", "json").Output()
	if err != nil || !strings.Contains(string(out), `"`+env["EZGHCLOUD_ORGANIZATION_ID"]+`"`) {
		t.Fatalf("the dev profile doesn't list organization %s: %v", env["EZGHCLOUD_ORGANIZATION_ID"], err)
	}
	config := func(size int) string {
		return fmt.Sprintf(`
resource "ezghcloud_database" "live" {
 project_id = %q
 name = "tf-live-database"
 engine = "postgresql"
 engine_version = "18"
 instance_type = "gs-1.large"
 storage_size_gib = %d
 subnet_id = %q
 final_backup_on_destroy = false
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], size, env["EZGHCLOUD_TEST_SUBNET_ID"])
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: config(20), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttrSet("ezghcloud_database.live", "endpoint_hostname"),
			resource.TestCheckResourceAttrSet("ezghcloud_database.live", "admin_secret_id"),
			resource.TestCheckResourceAttr("ezghcloud_database.live", "status", "available"))},
		{Config: config(30), Check: resource.TestCheckResourceAttr("ezghcloud_database.live", "storage_size_gib", "30")},
	}})
}
