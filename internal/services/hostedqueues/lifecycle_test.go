package hostedqueues_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"ezghcloud": providerserver.NewProtocol6WithError(provider.New("test")())}

// fakeAPI is hostedqueues-api as the provider sees it: every write answers 202 with an operation
// that has already succeeded. Objects are kept by their escaped path. The broker-origin topic
// app-events and queue celery@worker are there from the start: nothing may ever touch them (HC7).
type fakeAPI struct {
	mu       sync.Mutex
	objects  map[string]map[string]any
	requests []string
	patches  map[string][]map[string]any
	ops      int
}

const prefix = "/v1/organizations/org_test/projects/prj_test/clusters"

func newFake() *fakeAPI {
	f := &fakeAPI{objects: map[string]map[string]any{}, patches: map[string][]map[string]any{}}
	f.objects[prefix+"/hqc_kkkkkkkkkkkk/topics/app-events"] = map[string]any{"name": "app-events", "origin": "broker", "partitions": 1, "replicationFactor": 1, "config": map[string]any{}}
	f.objects[prefix+"/hqc_rrrrrrrrrrrr/vhosts/default/queues/celery%40worker"] = map[string]any{"name": "celery@worker", "origin": "broker", "vhost": "/"}
	return f
}

func (f *fakeAPI) accepted(w http.ResponseWriter, resourceName string) {
	f.ops++
	w.Header().Set("Location", fmt.Sprintf("/v1/organizations/org_test/operations/op-%d", f.ops))
	w.WriteHeader(202)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("op-%d", f.ops), "status": "running", "target": map[string]any{"resourceName": resourceName}})
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	f.requests = append(f.requests, r.Method+" "+p)
	w.Header().Set("Content-Type", "application/json")
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	switch {
	case p == "/v1/organizations/org_test/projects/prj_test":
		_, _ = w.Write([]byte(`{"id":"prj_test"}`))
	case strings.HasPrefix(p, "/v1/organizations/org_test/operations/"):
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	case r.Method == "POST" && p == prefix:
		id := "hqc_kkkkkkkkkkkk"
		if body["engine"] == "rabbitmq" {
			id = "hqc_rrrrrrrrrrrr"
		}
		f.objects[prefix+"/"+id] = cluster(id, body)
		f.objects[prefix+"/"+id+"/users/app"] = map[string]any{"name": "app", "status": "available", "secret": "sec_app", "permissions": map[string]any{}}
		f.accepted(w, "ezgh:us-west-1:org_test:prj_test:"+id)
	case r.Method == "POST":
		name, _ := body["name"].(string)
		view := map[string]any{"origin": "api", "observedAt": "2026-11-14T00:00:00.000Z", "readOnly": false, "pending": nil}
		for k, v := range body {
			view[k] = v
		}
		switch {
		case strings.HasSuffix(p, "/users"):
			view = map[string]any{"name": name, "status": "available", "secret": "sec_" + name, "failure": nil, "permissions": body["permissions"]}
			if body["permissions"] == nil {
				view["permissions"] = map[string]any{"preset": "readWrite"}
			}
		case strings.HasSuffix(p, "/topics"):
			if view["partitions"] == nil {
				view["partitions"] = 1
			}
			if view["replicationFactor"] == nil {
				view["replicationFactor"] = 1
			}
			config := map[string]any{"cleanup.policy": "delete"}
			if c, ok := body["config"].(map[string]any); ok {
				for k, v := range c {
					config[k] = v
				}
			}
			view["config"] = config
		case strings.HasSuffix(p, "/bindings"):
			name = "bk" + fmt.Sprint(f.ops)
			view["name"] = name
		case strings.HasSuffix(p, "/queues"):
			view["type"], view["durable"] = "quorum", true
		}
		if strings.Contains(p, "/vhosts/") {
			vhost, _ := url.PathUnescape(strings.Split(strings.SplitN(p, "/vhosts/", 2)[1], "/")[0])
			if vhost == "default" {
				vhost = "/"
			}
			view["vhost"] = vhost
			if view["arguments"] == nil && !strings.HasSuffix(p, "/policies") {
				view["arguments"] = map[string]any{}
			}
		}
		f.objects[p+"/"+url.PathEscape(name)] = view
		f.accepted(w, "ezgh:us-west-1:org_test:prj_test:"+strings.TrimPrefix(p, prefix+"/")+"/"+url.PathEscape(name))
	case f.objects[p] == nil:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Not found"}}`))
	case r.Method == "GET":
		_ = json.NewEncoder(w).Encode(f.objects[p])
	case r.Method == "PATCH":
		f.patches[p] = append(f.patches[p], body)
		o := f.objects[p]
		for k, v := range body {
			switch k {
			case "config":
				for ck, cv := range v.(map[string]any) {
					if cv == nil {
						delete(o["config"].(map[string]any), ck)
					} else {
						o["config"].(map[string]any)[ck] = cv
					}
				}
			case "raiseAppTopics":
			case "mode":
				o["mode"] = v
				e := o["endpoint"].(map[string]any)
				e["brokers"] = []any{map[string]any{"index": 2, "hostname": "kafka-x-2"}, map[string]any{"index": 0, "hostname": "kafka-x-0"}, map[string]any{"index": 1, "hostname": "kafka-x-1"}}
			case "plaintextPort":
				o[k] = v
				o["endpoint"].(map[string]any)["plaintextPort"] = 5672
			default:
				o[k] = v
			}
		}
		f.accepted(w, p)
	case r.Method == "DELETE":
		delete(f.objects, p)
		f.accepted(w, p)
	}
}

func cluster(id string, body map[string]any) map[string]any {
	c := map[string]any{"slug": id, "resourceName": "ezgh:us-west-1:org_test:prj_test:" + id, "status": "available", "vpc": "vpc_aaaaaaaaaaaa", "allowedSourceCidrs": []any{"10.0.0.0/16"},
		"maintenance": map[string]any{"windowUtc": "sun:05:00-sun:05:30"}, "unauthenticatedAccess": nil, "settings": map[string]any{},
		"endpoint": map[string]any{"hostname": "mq-x.hostedqueues.internal", "port": 5671, "plaintextPort": nil, "unauthenticatedPort": nil, "brokers": nil}}
	for k, v := range body {
		c[k] = v
	}
	c["description"] = body["description"]
	storage := body["storage"].(map[string]any)
	c["storage"] = map[string]any{"volumeType": "gp1", "sizeGiB": storage["sizeGiB"], "iops": 3000, "throughputMBps": 125}
	if body["engine"] == "kafka" {
		settings := map[string]any{"autoCreateTopics": true, "defaultRetentionMs": 604800000, "defaultPartitions": 1}
		if s, ok := body["settings"].(map[string]any); ok {
			for k, v := range s {
				settings[k] = v
			}
		}
		c["settings"] = settings
		c["unauthenticatedAccess"] = map[string]any{"enabled": false, "access": "readOnly"}
		c["endpoint"] = map[string]any{"hostname": "kafka-x", "port": 9093, "plaintextPort": nil, "unauthenticatedPort": nil, "brokers": []any{map[string]any{"index": 0, "hostname": "kafka-x-0"}}}
	}
	return c
}

func config(mode, version string, partitions int, topicConfig, preset, ttl, plaintext string) string {
	return fmt.Sprintf(`
resource "ezghcloud_hosted_queue_cluster" "kafka" {
  project_id       = "prj_test"
  name             = "events"
  engine           = "kafka"
  engine_version   = %[2]q
  mode             = %[1]q
  raise_app_topics = true
  instance_type    = "gp-1.small"
  storage_size_gib = 20
  subnet_id        = "sbn_aaaaaaaaaaaa"
  settings         = { auto_create_topics = false }
}
resource "ezghcloud_hosted_queue_user" "svc" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.kafka.id
  name       = "svc"
  preset     = %[5]q
}
resource "ezghcloud_hosted_queue_kafka_topic" "ingest" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.kafka.id
  name       = "ingest"
  partitions = %[3]d
  config     = %[4]s
}
resource "ezghcloud_hosted_queue_cluster" "mq" {
  project_id       = "prj_test"
  name             = "jobs"
  engine           = "rabbitmq"
  engine_version   = "4.3"
  instance_type    = "gp-1.small"
  storage_size_gib = 20
  subnet_id        = "sbn_aaaaaaaaaaaa"
  plaintext_port   = %[7]s
}
resource "ezghcloud_hosted_queue_rabbitmq_vhost" "app" {
  project_id         = "prj_test"
  cluster_id         = ezghcloud_hosted_queue_cluster.mq.id
  name               = "über"
  default_queue_type = "quorum"
}
resource "ezghcloud_hosted_queue_rabbitmq_queue" "ab" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  vhost      = ezghcloud_hosted_queue_rabbitmq_vhost.app.name
  name       = "a/b"
  arguments  = jsonencode({ "x-max-length" = 1000 })
}
resource "ezghcloud_hosted_queue_rabbitmq_queue" "jobs" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  vhost      = "/"
  name       = "jobs"
}
resource "ezghcloud_hosted_queue_rabbitmq_exchange" "events" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  vhost      = ezghcloud_hosted_queue_rabbitmq_vhost.app.name
  name       = "events"
  type       = "topic"
}
resource "ezghcloud_hosted_queue_rabbitmq_binding" "events_ab" {
  project_id       = "prj_test"
  cluster_id       = ezghcloud_hosted_queue_cluster.mq.id
  vhost            = ezghcloud_hosted_queue_rabbitmq_vhost.app.name
  source           = ezghcloud_hosted_queue_rabbitmq_exchange.events.name
  destination      = ezghcloud_hosted_queue_rabbitmq_queue.ab.name
  destination_type = "queue"
  routing_key      = "x.#"
}
resource "ezghcloud_hosted_queue_rabbitmq_policy" "ttl" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  vhost      = ezghcloud_hosted_queue_rabbitmq_vhost.app.name
  name       = "ttl"
  pattern    = ".*"
  definition = jsonencode({ "message-ttl" = %[6]s })
}
resource "ezghcloud_hosted_queue_user" "worker" {
  project_id = "prj_test"
  cluster_id = ezghcloud_hosted_queue_cluster.mq.id
  name       = "worker"
  vhosts     = [{ vhost = ezghcloud_hosted_queue_rabbitmq_vhost.app.name, preset = "readWrite" }]
}
data "ezghcloud_hosted_queue_cluster" "kafka" {
  project_id = "prj_test"
  id         = ezghcloud_hosted_queue_cluster.kafka.id
}
`, mode, version, partitions, topicConfig, preset, ttl, plaintext)
}

// TestAccHostedQueuesLifecycle creates a Kafka and a RabbitMQ cluster with a user and objects of
// every kind, grows Kafka to replicated and upgrades it, changes objects in place, and destroys it
// all. Every call names its own object; the apps' objects are never read, changed or deleted.
func TestAccHostedQueuesLifecycle(t *testing.T) {
	t.Setenv("EZGHCLOUD_PREVIEW_HOSTED_QUEUES", "1")
	f := newFake()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	t.Setenv("EZGHCLOUD_API_TOKEN", "t")
	t.Setenv("EZGHCLOUD_ORGANIZATION_ID", "org_test")
	providerBlock := fmt.Sprintf(`provider "ezghcloud" {
  endpoints = { hostedqueues = %[1]q, orgs = %[1]q }
}
`, s.URL)
	kafka := prefix + "/hqc_kkkkkkkkkkkk"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, req := range f.requests {
				if strings.Contains(req, "app-events") || strings.Contains(req, "celery") {
					return fmt.Errorf("an app's object was touched: %s", req)
				}
				if strings.HasSuffix(req, "/topics") && strings.HasPrefix(req, "GET") {
					return fmt.Errorf("a list was read: %s", req)
				}
			}
			for p := range f.objects {
				if !strings.HasSuffix(p, "/app-events") && !strings.HasSuffix(p, "/celery%40worker") && !strings.HasSuffix(p, "/users/app") {
					return fmt.Errorf("%s is still there", p)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: providerBlock + config("single", "4.3", 3, `{ "retention.ms" = "3600000" }`, "readOnly", "60000", "false"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "id", "hqc_kkkkkkkkkkkk"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "settings.auto_create_topics", "false"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "settings.default_partitions", "1"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "unauthenticated_access.enabled", "false"),
				resource.TestCheckNoResourceAttr("ezghcloud_hosted_queue_cluster.mq", "settings"),
				resource.TestCheckNoResourceAttr("ezghcloud_hosted_queue_cluster.mq", "unauthenticated_access"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_user.svc", "secret_id", "sec_svc"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_kafka_topic.ingest", "config.%", "1"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_kafka_topic.ingest", "origin", "api"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_rabbitmq_queue.ab", "id", "hqc_rrrrrrrrrrrr/über/a/b"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_rabbitmq_queue.jobs", "type", "quorum"),
				resource.TestCheckResourceAttrSet("ezghcloud_hosted_queue_rabbitmq_binding.events_ab", "key"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_user.worker", "vhosts.0.vhost", "über"),
				resource.TestCheckResourceAttr("data.ezghcloud_hosted_queue_cluster.kafka", "broker_hostnames.#", "1"),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					// Names reach the API as one escaped segment each.
					for _, want := range []string{"/vhosts/%C3%BCber/queues/a%2Fb", "/vhosts/default/queues/jobs"} {
						if f.objects[prefix+"/hqc_rrrrrrrrrrrr"+want] == nil {
							return fmt.Errorf("no object at %s", want)
						}
					}
					return nil
				})},
			{Config: providerBlock + config("replicated", "4.4", 6, "null", "readWrite", "120000", "true"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "mode", "replicated"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "engine_version", "4.4"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.kafka", "broker_hostnames.2", "kafka-x-2"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_cluster.mq", "endpoint_plaintext_port", "5672"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_kafka_topic.ingest", "partitions", "6"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_user.svc", "preset", "readWrite"),
				resource.TestCheckResourceAttr("ezghcloud_hosted_queue_rabbitmq_policy.ttl", "definition", `{"message-ttl":120000}`),
				func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					// Growth and the upgrade go on their own, in that order.
					want := []map[string]any{{"mode": "replicated", "raiseAppTopics": true}, {"engineVersion": "4.4"}}
					if got := f.patches[kafka]; !reflect.DeepEqual(got, want) {
						return fmt.Errorf("cluster patches %v", got)
					}
					// Removing a config key returns it to the cluster's default.
					want = []map[string]any{{"partitions": float64(6), "config": map[string]any{"retention.ms": nil}}}
					if got := f.patches[kafka+"/topics/ingest"]; !reflect.DeepEqual(got, want) {
						return fmt.Errorf("topic patches %v", got)
					}
					return nil
				})},
		}})
}
