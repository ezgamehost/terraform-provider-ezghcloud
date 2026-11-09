package cdn_test

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestLiveCDNDistribution makes one distribution with no domains on real EZGH and destroys it. It
// runs only with TF_ACC=1 and EZGH_PROFILE=dev, which CI never sets: the provider has no profiles,
// so this is the explicit opt-in. Not run before cdn-api is deployed (Gate 2, the platform org).
func TestLiveCDNDistribution(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	t.Setenv("EZGHCLOUD_PREVIEW_CDN", "1")
	env := map[string]string{}
	for _, k := range []string{"EZGHCLOUD_API_TOKEN", "EZGHCLOUD_ORGANIZATION_ID", "EZGHCLOUD_TEST_PROJECT_ID", "EZGHCLOUD_TEST_ORIGIN_HOST"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Fatalf("%s is required", k)
		}
	}
	out, err := exec.Command("ezgh", "--profile", "dev", "orgs", "list", "-o", "json").Output()
	if err != nil || !strings.Contains(string(out), `"`+env["EZGHCLOUD_ORGANIZATION_ID"]+`"`) {
		t.Fatalf("the dev profile doesn't list organization %s: %v", env["EZGHCLOUD_ORGANIZATION_ID"], err)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: fmt.Sprintf(`
resource "ezghcloud_cdn_distribution" "live" {
 project_id = %q
 name = "tf-live-cdn"
 origin = { host = %q }
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_ORIGIN_HOST"]), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestMatchResourceAttr("ezghcloud_cdn_distribution.live", "default_hostname", regexpDefault),
			resource.TestCheckResourceAttr("ezghcloud_cdn_distribution.live", "origin.port", "443"))},
	}})
}

var regexpDefault = regexp.MustCompile(`^d[a-z0-9]{12}\.ezghcdn\.com$`)
