package databases_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestLiveDatabaseLifecycle creates one gs-1.large database on real EZGH, adds a logical database and
// a user, checks the user's Secret exists, destroys them all with no final backup, and checks the
// Secret is gone. It runs only with TF_ACC=1 and EZGH_PROFILE=dev, which CI never sets: the provider has no
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
	config := func(size int, users bool) string {
		extra := ""
		if users {
			extra = fmt.Sprintf(`
resource "ezghcloud_database_logical_database" "shop" {
 project_id = %[1]q
 database_id = ezghcloud_database.live.id
 name = "shop"
}
resource "ezghcloud_database_user" "app" {
 project_id = %[1]q
 database_id = ezghcloud_database.live.id
 name = "app"
 logical_database = ezghcloud_database_logical_database.shop.name
 preset = "readWrite"
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"])
		}
		return extra + fmt.Sprintf(`
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
	// The username field proves the user's Secret exists without reading its password.
	var secret string
	readSecret := func() error {
		return exec.Command("ezgh", "--profile", "dev", "secrets", "read", secret, "--project", env["EZGHCLOUD_TEST_PROJECT_ID"], "--field", "username", "--format", "raw").Run()
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if secret == "" || readSecret() == nil {
				return fmt.Errorf("the app user's Secret %q is still there", secret)
			}
			return nil
		}, Steps: []resource.TestStep{
			{Config: config(20, false), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("ezghcloud_database.live", "endpoint_hostname"),
				resource.TestCheckResourceAttrSet("ezghcloud_database.live", "admin_secret_id"),
				resource.TestCheckResourceAttr("ezghcloud_database.live", "status", "available"))},
			{Config: config(30, false), Check: resource.TestCheckResourceAttr("ezghcloud_database.live", "storage_size_gib", "30")},
			{Config: config(30, true), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_database_user.app", "status", "available"),
				func(s *terraform.State) error {
					secret = s.RootModule().Resources["ezghcloud_database_user.app"].Primary.Attributes["secret_id"]
					if err := readSecret(); err != nil {
						return fmt.Errorf("the app user's Secret %q: %w", secret, err)
					}
					return nil
				})},
		}})
}
