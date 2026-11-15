package hostedqueues

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func schemaOf(r resource.Resource) schema.Schema {
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr.Schema
}

// replaces is whether a change to the string attribute replaces the resource.
func replaces(t *testing.T, s schema.Schema, name string) bool {
	t.Helper()
	a, ok := s.Attributes[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s isn't a string attribute", name)
	}
	for _, m := range a.PlanModifiers {
		if strings.Contains(m.Description(context.Background()), "destroy and recreate") {
			return true
		}
	}
	return false
}

func TestSchemasReplaceOnlyWhatMoves(t *testing.T) {
	s := schemaOf(&clusterResource{})
	for _, name := range []string{"engine", "subnet_id", "project_id"} {
		if !replaces(t, s, name) {
			t.Errorf("cluster %s doesn't force replacement", name)
		}
	}
	for _, name := range []string{"engine_version", "mode", "instance_type", "name"} {
		if replaces(t, s, name) {
			t.Errorf("cluster %s forces replacement", name)
		}
	}
	u := schemaOf(&userResource{})
	for _, name := range []string{"name", "cluster_id", "project_id"} {
		if !replaces(t, u, name) {
			t.Errorf("user %s doesn't force replacement", name)
		}
	}
	if replaces(t, u, "preset") {
		t.Error("user preset forces replacement")
	}
	if a := u.Attributes["secret_id"]; !a.IsComputed() || a.IsOptional() || a.IsRequired() {
		t.Error("secret_id isn't computed only")
	}
	for _, r := range []resource.Resource{&clusterResource{}, &userResource{}, newTopic(), newVhost(), newQueue(), newExchange(), newBinding(), newPolicy()} {
		for name, a := range schemaOf(r).Attributes {
			if strings.Contains(name, "password") || a.IsSensitive() {
				t.Errorf("%s holds a secret", name)
			}
		}
	}
	if p := schemaOf(newPolicy()); replaces(t, p, "pattern") || replaces(t, p, "definition") {
		t.Error("a policy's pattern or definition forces replacement")
	}
	if q := schemaOf(newQueue()); !replaces(t, q, "type") || !replaces(t, q, "vhost") {
		t.Error("a queue's type or vhost changes in place")
	}
}

func kafka() clusterModel {
	m := clusterModel{}
	m.ID, m.OrganizationID, m.ProjectID, m.ResourceName, m.Name = types.StringValue("hqc_123456789abc"), types.StringValue("org_test"), types.StringValue("prj_test"), types.StringValue("rn"), types.StringValue("events")
	m.Description, m.Tags, m.Engine, m.EngineVersion, m.Mode = types.StringNull(), types.MapValueMust(types.StringType, nil), types.StringValue("kafka"), types.StringValue("4.4"), types.StringValue("replicated")
	m.InstanceType, m.StorageSizeGiB, m.StorageIOPS, m.StorageThroughputMBps = types.StringValue("gp-1.small"), types.Int64Value(20), types.Int64Value(3000), types.Int64Value(125)
	m.VPCID, m.SubnetID, m.AllowedSourceCIDRs, m.PlaintextPort = types.StringValue("vpc_a"), types.StringValue("sbn_123456789abc"), types.ListValueMust(types.StringType, nil), types.BoolValue(false)
	m.UnauthenticatedAccess = types.ObjectValueMust(unauthTypes, map[string]attr.Value{"enabled": types.BoolValue(false), "access": types.StringValue("readOnly")})
	m.Settings = types.ObjectValueMust(settingsTypes, map[string]attr.Value{"auto_create_topics": types.BoolValue(true), "default_retention_ms": types.Int64Value(604800000), "default_partitions": types.Int64Value(1)})
	m.EndpointHostname, m.EndpointPort, m.EndpointPlaintextPort, m.EndpointUnauthenticatedPort = types.StringNull(), types.Int64Null(), types.Int64Null(), types.Int64Null()
	m.BrokerHostnames = types.ListValueMust(types.StringType, nil)
	m.MaintenanceWindowUTC, m.DeletionProtection, m.Status = types.StringValue("sun:05:00-sun:05:30"), types.BoolValue(false), types.StringValue("available")
	m.RaiseAppTopics, m.OperationID, m.OperationTimeout = types.BoolNull(), types.StringValue(""), types.StringValue("1m")
	return m
}

func asState(t *testing.T, r resource.Resource, m any) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: schemaOf(r)}
	if d := s.Set(context.Background(), m); d.HasError() {
		t.Fatal(d)
	}
	return s
}

// modifyPlan runs r's ModifyPlan with plan as plan and config, and state when it's non-nil.
func modifyPlan(t *testing.T, r resource.ResourceWithModifyPlan, state, plan any) resource.ModifyPlanResponse {
	t.Helper()
	p := asState(t, r, plan)
	req := resource.ModifyPlanRequest{Plan: tfsdk.Plan{Schema: p.Schema, Raw: p.Raw}, Config: tfsdk.Config{Schema: p.Schema, Raw: p.Raw}, State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
	if state != nil {
		req.State = asState(t, r, state)
	}
	resp := resource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(context.Background(), req, &resp)
	return resp
}

func TestClusterPlanRefusesWhatCantGoBack(t *testing.T) {
	r := &clusterResource{}
	old := kafka()
	for name, change := range map[string]func(*clusterModel){
		"downgrade": func(m *clusterModel) { m.EngineVersion = types.StringValue("4.3") },
		"shrink":    func(m *clusterModel) { m.StorageSizeGiB = types.Int64Value(10) },
		"single":    func(m *clusterModel) { m.Mode = types.StringValue("single") },
	} {
		next := old
		change(&next)
		if resp := modifyPlan(t, r, &old, &next); !resp.Diagnostics.HasError() {
			t.Errorf("%s wasn't refused", name)
		}
	}
	grow := old
	old.Mode = types.StringValue("single")
	if resp := modifyPlan(t, r, &old, &grow); resp.Diagnostics.HasError() {
		t.Errorf("growth: %v", resp.Diagnostics)
	}
	// A replacement makes a new cluster: going back to one node is fine then.
	moved := old
	moved.Mode, moved.SubnetID = types.StringValue("single"), types.StringValue("sbn_bbbbbbbbbbbb")
	replicated := kafka()
	if resp := modifyPlan(t, r, &replicated, &moved); resp.Diagnostics.HasError() {
		t.Errorf("replacement: %v", resp.Diagnostics)
	}
	mq := kafka()
	mq.Engine = types.StringValue("rabbitmq")
	if resp := modifyPlan(t, r, nil, &mq); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Summary(), "Kafka's") {
		t.Errorf("RabbitMQ with Kafka settings: %v", resp.Diagnostics)
	}
}

func TestClusterUpdateSendsOnlyWhatChanged(t *testing.T) {
	old := kafka()
	next := old
	next.Settings = types.ObjectValueMust(settingsTypes, map[string]attr.Value{"auto_create_topics": types.BoolValue(false), "default_retention_ms": types.Int64Unknown(), "default_partitions": types.Int64Value(1)})
	next.UnauthenticatedAccess = types.ObjectValueMust(unauthTypes, map[string]attr.Value{"enabled": types.BoolValue(true), "access": types.StringValue("readOnly")})
	next.EngineVersion, next.Mode = types.StringValue("9.9"), types.StringValue("single")
	body, d := updateBody(context.Background(), next, old)
	if d.HasError() {
		t.Fatal(d)
	}
	// Growth and an upgrade go in PATCHes of their own; settings send only the keys that changed.
	want := map[string]any{"settings": map[string]any{"autoCreateTopics": false}, "unauthenticatedAccess": map[string]any{"enabled": true}}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("update %v", body)
	}
}

func TestTopicPlanRefusesFewerPartitionsOrCopies(t *testing.T) {
	r := newTopic()
	old := topicModel{objectBase: objectBase{ID: types.StringValue("hqc_123456789abc/ingest"), OrganizationID: types.StringValue("org_test"), ProjectID: types.StringValue("prj_test"), ClusterID: types.StringValue("hqc_123456789abc"), Origin: types.StringValue("api")},
		Name: types.StringValue("ingest"), Partitions: types.Int64Value(6), ReplicationFactor: types.Int64Value(3), Config: types.MapNull(types.StringType)}
	for _, change := range []func(*topicModel){
		func(m *topicModel) { m.Partitions = types.Int64Value(3) },
		func(m *topicModel) { m.ReplicationFactor = types.Int64Value(1) },
	} {
		next := old
		change(&next)
		if resp := modifyPlan(t, r, &old, &next); !resp.Diagnostics.HasError() {
			t.Error("a decrease wasn't refused")
		}
	}
	renamed := old
	renamed.Name, renamed.Partitions = types.StringValue("ingest-2"), types.Int64Value(1)
	if resp := modifyPlan(t, r, &old, &renamed); resp.Diagnostics.HasError() {
		t.Errorf("a replacement was checked as an update: %v", resp.Diagnostics)
	}
	more := old
	more.Partitions = types.Int64Value(12)
	if resp := modifyPlan(t, r, &old, &more); resp.Diagnostics.HasError() {
		t.Errorf("more partitions: %v", resp.Diagnostics)
	}
}

func TestJSONStateKeepsTheConfiguredText(t *testing.T) {
	prior := types.StringValue(`{ "x-max-length": 1000 }`)
	if got := jsonState(prior, []byte(`{"x-max-length":1000}`)); !got.Equal(prior) {
		t.Errorf("same meaning: %s", got)
	}
	if got := jsonState(prior, []byte(`{"x-max-length":5}`)); got.ValueString() != `{"x-max-length":5}` {
		t.Errorf("drift: %s", got)
	}
	if got := jsonState(types.StringNull(), []byte(`{}`)); !got.IsNull() {
		t.Errorf("nothing configured: %s", got)
	}
}
