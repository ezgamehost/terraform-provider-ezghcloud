package hostedqueues

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	keepReplace     = []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	keepReplaceBool = []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()}
)

func nameAttr(d string) schema.StringAttribute {
	return schema.StringAttribute{Required: true, PlanModifiers: replace, Description: d + " Changing it replaces the object."}
}
func argumentsAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Arguments as a JSON object (jsonencode), at most 32 entries. Changing them replaces the object."}
}

// --- Kafka topic ---

type topicModel struct {
	objectBase
	Name              types.String `tfsdk:"name"`
	Partitions        types.Int64  `tfsdk:"partitions"`
	ReplicationFactor types.Int64  `tfsdk:"replication_factor"`
	Config            types.Map    `tfsdk:"config"`
}

func newTopic() *objectResource[topicModel, *topicModel] {
	return &objectResource[topicModel, *topicModel]{kind: objectKind{typeName: "hosted_queue_kafka_topic", collection: "topics", nameAttr: "name",
		description: "A topic on a Kafka cluster. Destroying it deletes this topic and its messages, nothing else; topics apps create are never touched unless imported.",
		attrs: map[string]schema.Attribute{
			"name":               nameAttr("The topic's name."),
			"partitions":         schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: "Partitions, 1 to 1000 (the cluster's default_partitions when left out). Grows in place; fewer is refused at plan."},
			"replication_factor": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: "Copies of each partition, 1 to 3. Left out, the cluster's default, and three once the cluster grows to replicated. Raised in place; lowering it is refused at plan."},
			"config":             schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Topic settings as Kafka's strings: retention.ms, retention.bytes, cleanup.policy, min.insync.replicas, max.message.bytes, compression.type, segment.bytes. Only configured keys are read back; removing one returns it to the cluster's default."},
		}}}
}
func (m *topicModel) where() (string, string) { return "", m.Name.ValueString() }
func (m *topicModel) setName(n string)        { m.Name = types.StringValue(n) }
func (m *topicModel) createBody(ctx context.Context) (map[string]any, diag.Diagnostics) {
	body := map[string]any{"name": m.Name.ValueString()}
	setInt(body, "partitions", m.Partitions)
	setInt(body, "replicationFactor", m.ReplicationFactor)
	var d diag.Diagnostics
	if known(m.Config) {
		config, cd := stringMap(ctx, m.Config)
		d.Append(cd...)
		body["config"] = config
	}
	return body, d
}
func (m *topicModel) updateBody(ctx context.Context, o brokerObject) (map[string]any, diag.Diagnostics) {
	old := o.(*topicModel)
	var d diag.Diagnostics
	body := map[string]any{}
	if known(m.Partitions) && !m.Partitions.Equal(old.Partitions) {
		body["partitions"] = m.Partitions.ValueInt64()
	}
	if known(m.ReplicationFactor) && !m.ReplicationFactor.Equal(old.ReplicationFactor) {
		body["replicationFactor"] = m.ReplicationFactor.ValueInt64()
	}
	if !m.Config.Equal(old.Config) {
		now, was := map[string]string{}, map[string]string{}
		if known(m.Config) {
			now, d = stringMap(ctx, m.Config)
		}
		if known(old.Config) {
			var od diag.Diagnostics
			was, od = stringMap(ctx, old.Config)
			d.Append(od...)
		}
		config := map[string]any{}
		for k, v := range now {
			if was[k] != v {
				config[k] = v
			}
		}
		for k := range was {
			if _, ok := now[k]; !ok {
				config[k] = nil // back to the cluster's default
			}
		}
		if len(config) > 0 {
			body["config"] = config
		}
	}
	return body, d
}
func (m *topicModel) checkPlan(o brokerObject, d *diag.Diagnostics) {
	old := o.(*topicModel)
	if known(m.Partitions) && known(old.Partitions) && m.Partitions.ValueInt64() < old.Partitions.ValueInt64() {
		d.AddAttributeError(path.Root("partitions"), "Partitions can't be removed", fmt.Sprintf("partitions must be at least %d.", old.Partitions.ValueInt64()))
	}
	if known(m.ReplicationFactor) && known(old.ReplicationFactor) && m.ReplicationFactor.ValueInt64() < old.ReplicationFactor.ValueInt64() {
		d.AddAttributeError(path.Root("replication_factor"), "Copies can't be removed", fmt.Sprintf("replication_factor must be at least %d.", old.ReplicationFactor.ValueInt64()))
	}
}
func (m *topicModel) fill(v map[string]json.RawMessage) {
	m.Partitions, m.ReplicationFactor = rawInt(v["partitions"]), rawInt(v["replicationFactor"])
	if known(m.Config) {
		var config map[string]string
		_ = json.Unmarshal(v["config"], &config)
		keep := map[string]string{}
		for k := range m.Config.Elements() {
			if value, ok := config[k]; ok {
				keep[k] = value
			}
		}
		m.Config = mapValue(context.Background(), keep)
	}
}

// --- RabbitMQ vhost ---

type vhostModel struct {
	objectBase
	Name             types.String `tfsdk:"name"`
	DefaultQueueType types.String `tfsdk:"default_queue_type"`
	MaxQueues        types.Int64  `tfsdk:"max_queues"`
	MaxConnections   types.Int64  `tfsdk:"max_connections"`
}

func newVhost() *objectResource[vhostModel, *vhostModel] {
	return &objectResource[vhostModel, *vhostModel]{kind: objectKind{typeName: "hosted_queue_rabbitmq_vhost", collection: "vhosts", nameAttr: "name",
		description: "A vhost on a RabbitMQ cluster. New queues are quorum queues unless the app declares another type. Destroying it deletes this vhost alone, and is refused while it holds queues.",
		attrs: map[string]schema.Attribute{
			"name":               nameAttr("The vhost's name; default is reserved."),
			"default_queue_type": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "quorum, classic or stream: the type of a queue declared without one. Changes in place."},
			"max_queues":         schema.Int64Attribute{Optional: true, Description: "Most queues the vhost may hold; null for no limit. Changes in place."},
			"max_connections":    schema.Int64Attribute{Optional: true, Description: "Most connections to the vhost; null for no limit. Changes in place."},
		}}}
}
func (m *vhostModel) where() (string, string) { return "", m.Name.ValueString() }
func (m *vhostModel) setName(n string)        { m.Name = types.StringValue(n) }
func (m *vhostModel) limits() map[string]any {
	return map[string]any{"maxQueues": m.MaxQueues.ValueInt64Pointer(), "maxConnections": m.MaxConnections.ValueInt64Pointer()}
}
func (m *vhostModel) createBody(context.Context) (map[string]any, diag.Diagnostics) {
	body := map[string]any{"name": m.Name.ValueString()}
	setString(body, "defaultQueueType", m.DefaultQueueType)
	if known(m.MaxQueues) || known(m.MaxConnections) {
		body["limits"] = m.limits()
	}
	return body, nil
}
func (m *vhostModel) updateBody(_ context.Context, o brokerObject) (map[string]any, diag.Diagnostics) {
	old := o.(*vhostModel)
	body := map[string]any{}
	if known(m.DefaultQueueType) && !m.DefaultQueueType.Equal(old.DefaultQueueType) {
		body["defaultQueueType"] = m.DefaultQueueType.ValueString()
	}
	if !m.MaxQueues.Equal(old.MaxQueues) || !m.MaxConnections.Equal(old.MaxConnections) {
		body["limits"] = m.limits()
	}
	return body, nil
}
func (m *vhostModel) fill(v map[string]json.RawMessage) {
	m.DefaultQueueType = rawString(v["defaultQueueType"])
	var limits struct{ MaxQueues, MaxConnections json.RawMessage }
	_ = json.Unmarshal(v["limits"], &limits)
	m.MaxQueues, m.MaxConnections = rawInt(limits.MaxQueues), rawInt(limits.MaxConnections)
}

// --- RabbitMQ queue ---

type queueModel struct {
	objectBase
	Vhost     types.String `tfsdk:"vhost"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Durable   types.Bool   `tfsdk:"durable"`
	Arguments types.String `tfsdk:"arguments"`
}

func newQueue() *objectResource[queueModel, *queueModel] {
	return &objectResource[queueModel, *queueModel]{kind: objectKind{typeName: "hosted_queue_rabbitmq_queue", collection: "queues", vhosted: true, nameAttr: "name",
		description: "A queue on a RabbitMQ cluster. New queues are quorum queues unless the app declares another type. Destroying it deletes this queue and its messages, nothing else; queues apps declare are never touched unless imported.",
		attrs: map[string]schema.Attribute{
			"name":      nameAttr("The queue's name."),
			"type":      schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepReplace, Description: "quorum, classic or stream; the vhost's default_queue_type when left out. Changing it replaces the queue."},
			"durable":   schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepReplaceBool, Description: "Whether the queue survives a broker restart. Changing it replaces the queue."},
			"arguments": argumentsAttr(),
		}}}
}
func (m *queueModel) where() (string, string) { return m.Vhost.ValueString(), m.Name.ValueString() }
func (m *queueModel) setName(n string)        { m.Name = types.StringValue(n) }
func (m *queueModel) createBody(context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{"name": m.Name.ValueString()}
	setString(body, "type", m.Type)
	setBool(body, "durable", m.Durable)
	if args := jsonObject(m.Arguments, "arguments", &d); args != nil {
		body["arguments"] = args
	}
	return body, d
}
func (*queueModel) updateBody(context.Context, brokerObject) (map[string]any, diag.Diagnostics) {
	return nil, nil
}

// fill reads the queue. The broker shows the x-queue-type every queue is declared with among its
// arguments; it is the type attribute's, so arguments show it only when configured there.
func (m *queueModel) fill(v map[string]json.RawMessage) {
	args := v["arguments"]
	var parsed, configured map[string]any
	if json.Unmarshal(args, &parsed) == nil && parsed != nil {
		_ = json.Unmarshal([]byte(m.Arguments.ValueString()), &configured)
		if _, ok := configured["x-queue-type"]; !ok {
			delete(parsed, "x-queue-type")
			args, _ = json.Marshal(parsed)
		}
	}
	m.Type, m.Durable, m.Arguments = rawString(v["type"]), rawBool(v["durable"]), jsonState(m.Arguments, args)
}

// --- RabbitMQ exchange ---

type exchangeModel struct {
	objectBase
	Vhost     types.String `tfsdk:"vhost"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Durable   types.Bool   `tfsdk:"durable"`
	Arguments types.String `tfsdk:"arguments"`
}

func newExchange() *objectResource[exchangeModel, *exchangeModel] {
	return &objectResource[exchangeModel, *exchangeModel]{kind: objectKind{typeName: "hosted_queue_rabbitmq_exchange", collection: "exchanges", vhosted: true, nameAttr: "name",
		description: "An exchange on a RabbitMQ cluster. Destroying it deletes this exchange alone.",
		attrs: map[string]schema.Attribute{
			"name":      nameAttr("The exchange's name."),
			"type":      schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "direct, fanout, topic, headers or a plugin's x- type. Changing it replaces the exchange."},
			"durable":   schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepReplaceBool, Description: "Whether the exchange survives a broker restart. Changing it replaces the exchange."},
			"arguments": argumentsAttr(),
		}}}
}
func (m *exchangeModel) where() (string, string) { return m.Vhost.ValueString(), m.Name.ValueString() }
func (m *exchangeModel) setName(n string)        { m.Name = types.StringValue(n) }
func (m *exchangeModel) createBody(context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{"name": m.Name.ValueString(), "type": m.Type.ValueString()}
	setBool(body, "durable", m.Durable)
	if args := jsonObject(m.Arguments, "arguments", &d); args != nil {
		body["arguments"] = args
	}
	return body, d
}
func (*exchangeModel) updateBody(context.Context, brokerObject) (map[string]any, diag.Diagnostics) {
	return nil, nil
}
func (m *exchangeModel) fill(v map[string]json.RawMessage) {
	m.Type, m.Durable, m.Arguments = rawString(v["type"]), rawBool(v["durable"]), jsonState(m.Arguments, v["arguments"])
}

// --- RabbitMQ binding ---

type bindingModel struct {
	objectBase
	Vhost           types.String `tfsdk:"vhost"`
	Key             types.String `tfsdk:"key"`
	Source          types.String `tfsdk:"source"`
	Destination     types.String `tfsdk:"destination"`
	DestinationType types.String `tfsdk:"destination_type"`
	RoutingKey      types.String `tfsdk:"routing_key"`
	Arguments       types.String `tfsdk:"arguments"`
}

func newBinding() *objectResource[bindingModel, *bindingModel] {
	return &objectResource[bindingModel, *bindingModel]{kind: objectKind{typeName: "hosted_queue_rabbitmq_binding", collection: "bindings", vhosted: true, nameAttr: "key",
		description: "A binding from an exchange to a queue or exchange on a RabbitMQ cluster. Any change replaces it. Destroying it deletes this binding alone.",
		attrs: map[string]schema.Attribute{
			"key":              schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "The binding's key, assigned by EZGH from its fields."},
			"source":           nameAttr("The exchange it routes from."),
			"destination":      nameAttr("The queue or exchange it routes to."),
			"destination_type": nameAttr("queue or exchange."),
			"routing_key":      schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), PlanModifiers: replace, Description: "The routing key; empty when left out. Changing it replaces the binding."},
			"arguments":        argumentsAttr(),
		}}}
}
func (m *bindingModel) where() (string, string) { return m.Vhost.ValueString(), m.Key.ValueString() }
func (m *bindingModel) setName(n string)        { m.Key = types.StringValue(n) }
func (m *bindingModel) createBody(context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{"source": m.Source.ValueString(), "destination": m.Destination.ValueString(), "destinationType": m.DestinationType.ValueString(), "routingKey": m.RoutingKey.ValueString()}
	if args := jsonObject(m.Arguments, "arguments", &d); args != nil {
		body["arguments"] = args
	}
	return body, d
}
func (*bindingModel) updateBody(context.Context, brokerObject) (map[string]any, diag.Diagnostics) {
	return nil, nil
}
func (m *bindingModel) fill(v map[string]json.RawMessage) {
	m.Source, m.Destination, m.DestinationType = rawString(v["source"]), rawString(v["destination"]), rawString(v["destinationType"])
	if rk := rawString(v["routingKey"]); known(rk) {
		m.RoutingKey = rk
	}
	m.Arguments = jsonState(m.Arguments, v["arguments"])
}

// --- RabbitMQ policy ---

type policyModel struct {
	objectBase
	Vhost      types.String `tfsdk:"vhost"`
	Name       types.String `tfsdk:"name"`
	Pattern    types.String `tfsdk:"pattern"`
	ApplyTo    types.String `tfsdk:"apply_to"`
	Priority   types.Int64  `tfsdk:"priority"`
	Definition types.String `tfsdk:"definition"`
}

func newPolicy() *objectResource[policyModel, *policyModel] {
	return &objectResource[policyModel, *policyModel]{kind: objectKind{typeName: "hosted_queue_rabbitmq_policy", collection: "policies", vhosted: true, nameAttr: "name",
		description: "A policy on a RabbitMQ cluster's vhost. Destroying it deletes this policy alone.",
		attrs: map[string]schema.Attribute{
			"name":       nameAttr("The policy's name."),
			"pattern":    schema.StringAttribute{Required: true, Description: "Regular expression matched against queue and exchange names. Changes in place."},
			"apply_to":   schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "all, queues, exchanges, classic_queues, quorum_queues or streams. Changes in place."},
			"priority":   schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: "0 to 999; the highest matching policy applies. Changes in place."},
			"definition": schema.StringAttribute{Required: true, Description: "The policy as a JSON object (jsonencode) of max-length, max-length-bytes, message-ttl, expires, overflow, dead-letter-exchange, dead-letter-routing-key, delivery-limit or queue-leader-locator. Changes in place."},
		}}}
}
func (m *policyModel) where() (string, string) { return m.Vhost.ValueString(), m.Name.ValueString() }
func (m *policyModel) setName(n string)        { m.Name = types.StringValue(n) }
func (m *policyModel) createBody(context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{"name": m.Name.ValueString(), "pattern": m.Pattern.ValueString(), "definition": jsonObject(m.Definition, "definition", &d)}
	setString(body, "applyTo", m.ApplyTo)
	setInt(body, "priority", m.Priority)
	return body, d
}
func (m *policyModel) updateBody(_ context.Context, o brokerObject) (map[string]any, diag.Diagnostics) {
	old := o.(*policyModel)
	var d diag.Diagnostics
	body := map[string]any{}
	if !m.Pattern.Equal(old.Pattern) {
		body["pattern"] = m.Pattern.ValueString()
	}
	if known(m.ApplyTo) && !m.ApplyTo.Equal(old.ApplyTo) {
		body["applyTo"] = m.ApplyTo.ValueString()
	}
	if known(m.Priority) && !m.Priority.Equal(old.Priority) {
		body["priority"] = m.Priority.ValueInt64()
	}
	if !m.Definition.Equal(old.Definition) {
		body["definition"] = jsonObject(m.Definition, "definition", &d)
	}
	return body, d
}
func (m *policyModel) fill(v map[string]json.RawMessage) {
	m.Pattern, m.ApplyTo, m.Priority, m.Definition = rawString(v["pattern"]), rawString(v["applyTo"]), rawInt(v["priority"]), jsonState(m.Definition, v["definition"])
}
