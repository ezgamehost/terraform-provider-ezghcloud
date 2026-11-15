package warehouse_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestLiveWarehouseLifecycle creates a 1 × 1 warehouse on 26.3 on real EZGH with a user and a
// database, checks the users' Secrets exist, grows it to 2 shards × 2 replicas and upgrades it to
// 26.8, then destroys it all and checks the Secrets are gone. It runs only with TF_ACC=1 and
// EZGH_PROFILE=dev, which CI never sets: the provider has no profiles, so this is the explicit
// opt-in (Gates 2 and 3).
func TestLiveWarehouseLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	t.Setenv("EZGHCLOUD_PREVIEW_WAREHOUSE", "1")
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
	config := func(version string, shards, replicas int) string {
		return fmt.Sprintf(`
resource "ezghcloud_warehouse" "analytics" {
  project_id       = %[1]q
  name             = "tf-live-warehouse"
  engine_version   = %[3]q
  shards           = %[4]d
  replicas         = %[5]d
  instance_type    = "gp-1.large"
  storage_size_gib = 20
  subnet_id        = %[2]q
}
resource "ezghcloud_warehouse_user" "loader" {
  project_id   = %[1]q
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "loader"
  preset       = "readWrite"
}
resource "ezghcloud_warehouse_database" "events" {
  project_id   = %[1]q
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "Events_TF"
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_SUBNET_ID"], version, shards, replicas)
	}
	// The username field proves a Secret exists without reading its password.
	var secrets []string
	readSecret := func(secret string) error {
		return exec.Command("ezgh", "--profile", "dev", "secrets", "read", secret, "--project", env["EZGHCLOUD_TEST_PROJECT_ID"], "--field", "username", "--format", "raw").Run()
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			for _, secret := range secrets {
				// ezgh exits 5 on a 404.
				var exit *exec.ExitError
				if err := readSecret(secret); !errors.As(err, &exit) || exit.ExitCode() != 5 {
					return fmt.Errorf("Secret %q isn't gone: %v", secret, err)
				}
			}
			return nil
		}, Steps: []resource.TestStep{
			{Config: config("26.3", 1, 1), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "status", "available"),
				resource.TestCheckResourceAttrSet("ezghcloud_warehouse.analytics", "endpoint_hostname"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "endpoint_https_port", "8443"),
				resource.TestCheckNoResourceAttr("ezghcloud_warehouse.analytics", "keeper"),
				func(s *terraform.State) error {
					for _, k := range []string{"app_secret_id", "secret_id"} {
						for _, r := range []string{"ezghcloud_warehouse.analytics", "ezghcloud_warehouse_user.loader"} {
							secret := s.RootModule().Resources[r].Primary.Attributes[k]
							if secret == "" {
								continue
							}
							if err := readSecret(secret); err != nil {
								return fmt.Errorf("%s's Secret %q: %w", r, secret, err)
							}
							secrets = append(secrets, secret)
						}
					}
					return nil
				})},
			{Config: config("26.3", 2, 2), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "shards", "2"),
				resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "keeper", "dedicated"))},
			{Config: config("26.8", 2, 2), Check: resource.TestCheckResourceAttr("ezghcloud_warehouse.analytics", "engine_version", "26.8")},
		}})
}
