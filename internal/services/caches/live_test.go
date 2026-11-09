package caches_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// TestLiveCacheLifecycle creates one memory-only Valkey 8.1 cache on real EZGH with a user, checks
// the user's Secret exists, changes the user's preset and a parameter in place, upgrades the cache
// to 9.1, destroys it all and checks the Secret is gone. It runs only with TF_ACC=1 and
// EZGH_PROFILE=dev, which CI never sets: the provider has no profiles, so this is the explicit opt-in.
func TestLiveCacheLifecycle(t *testing.T) {
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
	config := func(version, preset, policy string) string {
		return fmt.Sprintf(`
resource "ezghcloud_cache" "live" {
 project_id = %[1]q
 name = "tf-live-cache"
 engine = "valkey"
 engine_version = %[3]q
 instance_type = "gs-1.large"
 subnet_id = %[2]q
 parameters = { "maxmemory-policy" = %[5]q }
}
resource "ezghcloud_cache_user" "app" {
 project_id = %[1]q
 cache_id = ezghcloud_cache.live.id
 name = "app"
 preset = %[4]q
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_SUBNET_ID"], version, preset, policy)
	}
	// The username field proves the user's Secret exists without reading its password.
	var secret string
	readSecret := func() error {
		return exec.Command("ezgh", "--profile", "dev", "secrets", "read", secret, "--project", env["EZGHCLOUD_TEST_PROJECT_ID"], "--field", "username", "--format", "raw").Run()
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			// ezgh exits 5 on a 404.
			var exit *exec.ExitError
			if err := readSecret(); secret == "" || !errors.As(err, &exit) || exit.ExitCode() != 5 {
				return fmt.Errorf("the app user's Secret %q isn't gone: %v", secret, err)
			}
			return nil
		}, Steps: []resource.TestStep{
			{Config: config("8.1", "readWrite", "allkeys-lru"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_cache.live", "status", "available"),
				resource.TestCheckResourceAttrSet("ezghcloud_cache.live", "endpoint_hostname"),
				resource.TestCheckResourceAttr("ezghcloud_cache.live", "endpoint_port", "6379"),
				resource.TestCheckResourceAttr("ezghcloud_cache_user.app", "status", "available"),
				func(s *terraform.State) error {
					secret = s.RootModule().Resources["ezghcloud_cache_user.app"].Primary.Attributes["secret_id"]
					if err := readSecret(); err != nil {
						return fmt.Errorf("the app user's Secret %q: %w", secret, err)
					}
					return nil
				})},
			{Config: config("8.1", "readOnly", "volatile-lru"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_cache_user.app", "preset", "readOnly"),
				resource.TestCheckResourceAttr("ezghcloud_cache.live", "parameters.maxmemory-policy", "volatile-lru"))},
			{Config: config("9.1", "readOnly", "volatile-lru"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_cache.live", "engine_version", "9.1"),
				resource.TestCheckResourceAttr("ezghcloud_cache.live", "status", "available"))},
		}})
}
