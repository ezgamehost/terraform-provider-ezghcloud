package hostedqueues_test

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

// TestLiveHostedQueuesLifecycle creates a single-node Kafka 4.3 cluster on real EZGH with a user and
// a topic and a RabbitMQ 4.3 cluster with a vhost and a queue, checks the users' Secrets exist,
// grows Kafka to replicated, upgrades it to 4.4 and adds partitions, then destroys it all and checks
// the Secrets are gone. It runs only with TF_ACC=1 and EZGH_PROFILE=dev, which CI never sets: the
// provider has no profiles, so this is the explicit opt-in (Gates 2 and 3).
func TestLiveHostedQueuesLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("EZGH_PROFILE") != "dev" {
		t.Skip("set TF_ACC=1 and EZGH_PROFILE=dev to run against real EZGH")
	}
	t.Setenv("EZGHCLOUD_PREVIEW_HOSTED_QUEUES", "1")
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
	config := func(mode, version string, partitions int) string {
		return fmt.Sprintf(`
resource "ezghcloud_hosted_queue_cluster" "kafka" {
  project_id       = %[1]q
  name             = "tf-live-kafka"
  engine           = "kafka"
  engine_version   = %[4]q
  mode             = %[3]q
  raise_app_topics = true
  instance_type    = "gp-1.small"
  storage_size_gib = 20
  subnet_id        = %[2]q
}
resource "ezghcloud_hosted_queue_user" "svc" {
  project_id = %[1]q
  cluster_id = ezghcloud_hosted_queue_cluster.kafka.id
  name       = "svc"
  preset     = "readWrite"
}
resource "ezghcloud_hosted_queue_kafka_topic" "ingest" {
  project_id = %[1]q
  cluster_id = ezghcloud_hosted_queue_cluster.kafka.id
  name       = "ingest"
  partitions = %[5]d
}
resource "ezghcloud_hosted_queue_cluster" "mq" {
  project_id       = %[1]q
  name             = "tf-live-rabbitmq"
  engine           = "rabbitmq"
  engine_version   = "4.3"
  instance_type    = "gp-1.small"
  storage_size_gib = 20
  subnet_id        = %[2]q
}
resource "ezghcloud_hosted_queue_rabbitmq_vhost" "app" {
  project_id = %[1]q
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  name       = "app"
}
resource "ezghcloud_hosted_queue_rabbitmq_queue" "jobs" {
  project_id = %[1]q
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  vhost      = ezghcloud_hosted_queue_rabbitmq_vhost.app.name
  name       = "jobs"
}
resource "ezghcloud_hosted_queue_user" "worker" {
  project_id = %[1]q
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  name       = "worker"
  vhosts     = [{ vhost = ezghcloud_hosted_queue_rabbitmq_vhost.app.name, preset = "readWrite" }]
}
`, env["EZGHCLOUD_TEST_PROJECT_ID"], env["EZGHCLOUD_TEST_SUBNET_ID"], mode, version, partitions)
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
			{Config: config("single", "4.3", 3), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "status", "available"),
				resource.TestCheckResourceAttrSet("ezghcloud_hosted_queue_cluster.kafka", "endpoint_hostname"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "endpoint_port", "9093"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.mq", "endpoint_port", "5671"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_kafka_topic.ingest", "partitions", "3"),
				func(s *terraform.State) error {
					for _, user := range []string{"ezghcloud_hosted_queue_user.svc", "ezghcloud_hosted_queue_user.worker"} {
						secret := s.RootModule().Resources[user].Primary.Attributes["secret_id"]
						if err := readSecret(secret); err != nil {
							return fmt.Errorf("%s's Secret %q: %w", user, secret, err)
						}
						secrets = append(secrets, secret)
					}
					return nil
				})},
			{Config: config("replicated", "4.3", 3), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "mode", "replicated"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "broker_hostnames.#", "3"))},
			{Config: config("replicated", "4.4", 6), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "engine_version", "4.4"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_kafka_topic.ingest", "partitions", "6"))},
		}})
}
