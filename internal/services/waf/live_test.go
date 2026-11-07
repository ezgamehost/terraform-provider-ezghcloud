package waf_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestLiveWAFLifecycle makes a WAF on real EZGH, attaches it to an existing load balancer, and
// destroys only what it made (the load balancer stays). It runs only with TF_ACC=1 and
// EZGH_PROFILE=dev, which CI never sets: the provider has no profiles, so this is the explicit
// opt-in. Not run before waf-api is deployed.
func TestLiveWAFLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	env := map[string]string{}
	for _, k := range []string{"EZGHCLOUD_API_TOKEN", "EZGHCLOUD_ORGANIZATION_ID", "EZGHCLOUD_TEST_PROJECT_ID", "EZGHCLOUD_TEST_LOAD_BALANCER_ID"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Fatalf("%s is required", k)
		}
	}
	out, err := exec.Command("ezgh", "--profile", "dev", "orgs", "list", "-o", "json").Output()
	if err != nil || !strings.Contains(string(out), `"`+env["EZGHCLOUD_ORGANIZATION_ID"]+`"`) {
		t.Fatalf("the dev profile doesn't list organization %s: %v", env["EZGHCLOUD_ORGANIZATION_ID"], err)
	}
	config := func(mode string) string {
		return fmt.Sprintf(`
resource "ezghcloud_waf" "live" {
 project_id = %[1]q
 name = "tf-live-waf"
 mode = %[3]q
 managed_rule_sets = [{ name = "ezgh/core" }]
 rules = [{ name = "block-test-net", action = "block", conditions = [{ type = "ipSet", cidrs = ["192.0.2.0/24"] }] }]
}
resource "ezghcloud_load_balancer_waf" "live" {
 project_id = %[1]q
 load_balancer_id = %[2]q
 waf_id = ezghcloud_waf.live.id
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_LOAD_BALANCER_ID"], mode)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: config("log"), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("ezghcloud_waf.live", "status", "active"),
			resource.TestCheckResourceAttr("ezghcloud_waf.live", "managed_rule_sets.0.paranoia_level", "1"),
			resource.TestCheckResourceAttrPair("ezghcloud_load_balancer_waf.live", "waf_id", "ezghcloud_waf.live", "id"))},
		{Config: config("block"), Check: resource.TestCheckResourceAttr("ezghcloud_waf.live", "mode", "block")},
	}})
}
