package hostedqueues

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// clusterFields are what the API shows; the data source reads exactly these.
type clusterFields struct {
	ID                          types.String `tfsdk:"id"`
	OrganizationID              types.String `tfsdk:"organization_id"`
	ProjectID                   types.String `tfsdk:"project_id"`
	ResourceName                types.String `tfsdk:"resource_name"`
	Name                        types.String `tfsdk:"name"`
	Description                 types.String `tfsdk:"description"`
	Tags                        types.Map    `tfsdk:"tags"`
	Engine                      types.String `tfsdk:"engine"`
	EngineVersion               types.String `tfsdk:"engine_version"`
	Mode                        types.String `tfsdk:"mode"`
	InstanceType                types.String `tfsdk:"instance_type"`
	StorageSizeGiB              types.Int64  `tfsdk:"storage_size_gib"`
	StorageIOPS                 types.Int64  `tfsdk:"storage_iops"`
	StorageThroughputMBps       types.Int64  `tfsdk:"storage_throughput_mbps"`
	VPCID                       types.String `tfsdk:"vpc_id"`
	SubnetID                    types.String `tfsdk:"subnet_id"`
	AllowedSourceCIDRs          types.List   `tfsdk:"allowed_source_cidrs"`
	PlaintextPort               types.Bool   `tfsdk:"plaintext_port"`
	UnauthenticatedAccess       types.Object `tfsdk:"unauthenticated_access"`
	Settings                    types.Object `tfsdk:"settings"`
	EndpointHostname            types.String `tfsdk:"endpoint_hostname"`
	EndpointPort                types.Int64  `tfsdk:"endpoint_port"`
	EndpointPlaintextPort       types.Int64  `tfsdk:"endpoint_plaintext_port"`
	EndpointUnauthenticatedPort types.Int64  `tfsdk:"endpoint_unauthenticated_port"`
	BrokerHostnames             types.List   `tfsdk:"broker_hostnames"`
	MaintenanceWindowUTC        types.String `tfsdk:"maintenance_window_utc"`
	DeletionProtection          types.Bool   `tfsdk:"deletion_protection"`
	Status                      types.String `tfsdk:"status"`
}
type clusterModel struct {
	clusterFields
	RaiseAppTopics   types.Bool   `tfsdk:"raise_app_topics"`
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type brokerView struct {
	Index    int64
	Hostname string
}
type clusterView struct {
	Slug, ResourceName, Name, Engine, EngineVersion, Mode, InstanceType, VPC, Subnet, Status string
	Description                                                                              *string
	Tags                                                                                     map[string]string
	Storage                                                                                  struct{ SizeGiB, IOPS, ThroughputMBps int64 }
	AllowedSourceCidrs                                                                       []string
	PlaintextPort, DeletionProtection                                                        bool
	Endpoint                                                                                 *struct {
		Hostname                           string
		Port                               int64
		PlaintextPort, UnauthenticatedPort *int64
		Brokers                            []brokerView
	}
	UnauthenticatedAccess *struct {
		Enabled bool
		Access  string
	}
	Settings struct {
		AutoCreateTopics                      *bool
		DefaultRetentionMs, DefaultPartitions *int64
	}
	Maintenance struct{ WindowUtc string }
}

var (
	unauthTypes   = map[string]attr.Type{"enabled": types.BoolType, "access": types.StringType}
	settingsTypes = map[string]attr.Type{"auto_create_topics": types.BoolType, "default_retention_ms": types.Int64Type, "default_partitions": types.Int64Type}
	keepString    = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt       = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	keepBool      = []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	replace       = []planmodifier.String{stringplanmodifier.RequiresReplace()}
)

type clusterResource struct{ client *client.Client }

func (*clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hosted_queue_cluster"
}

func (*clusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: d, PlanModifiers: keepString}
	}
	optionalInt := func(d string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Description: d, PlanModifiers: keepInt}
	}
	keepObject := []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{Description: "A managed Kafka or RabbitMQ cluster in a subnet of your VPC: one node, or three that survive the loss of any one. Clients connect over TLS with a username and password. It comes with the user app, whose password is in a Secret. Destroying it deletes the cluster, every object in it and its users' Secrets.", Attributes: map[string]schema.Attribute{
		"id":                      computed("Cluster ID (hqc_)."),
		"organization_id":         computed("Organization managed by the configured provider."),
		"project_id":              schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the cluster.", PlanModifiers: replace},
		"resource_name":           computed("IAM resource name."),
		"name":                    schema.StringAttribute{Required: true, Description: "Cluster name. Changes in place."},
		"description":             schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."},
		"tags":                    schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"engine":                  schema.StringAttribute{Required: true, Description: "kafka or rabbitmq. Changing it replaces the cluster.", PlanModifiers: replace},
		"engine_version":          schema.StringAttribute{Required: true, Description: "Engine version, 4.3 or 4.4 on both engines. A newer version upgrades in place, one node at a time (one-way); an older one is refused at plan."},
		"mode":                    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("single"), Description: "single (one node) or replicated (three, surviving the loss of any one). single grows to replicated in place; replicated can't go back to single, so that change is refused at plan."},
		"raise_app_topics":        schema.BoolAttribute{Optional: true, Description: "Kafka: when mode grows to replicated, also give every topic an app created three copies. Read only with that change; topics created through EZGH without a replication factor and Kafka's internal topics are raised either way."},
		"instance_type":           schema.StringAttribute{Required: true, Description: "A gp-1, gs-1 or hp-1 instance type (hp-1 for a single node only). Changes in place, one node at a time."},
		"storage_size_gib":        schema.Int64Attribute{Required: true, Description: "Each node's storage in GiB, at least 10. Grows in place; shrinking is refused."},
		"storage_iops":            optionalInt("Each node's provisioned storage IOPS. Changes in place."),
		"storage_throughput_mbps": optionalInt("Each node's provisioned storage throughput in MB/s. Changes in place."),
		"vpc_id":                  computed("VPC of the subnet."),
		"subnet_id":               schema.StringAttribute{Required: true, Description: "Subnet (sbn_) the cluster is in. Changing it replaces the cluster.", PlanModifiers: replace},
		"allowed_source_cidrs":    schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "1 to 20 IPv4 CIDRs allowed to connect. Left out at create, the VPC's ranges; removing it later keeps the current list.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"plaintext_port":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Also serve password authentication without TLS inside the VPC (Kafka 9092 SASL_PLAINTEXT, RabbitMQ 5672 AMQP), for apps that can't do TLS. Changes in place."},
		"unauthenticated_access": schema.SingleNestedAttribute{Optional: true, Computed: true, PlanModifiers: keepObject, Description: "Kafka only: a TLS listener on 9091 with no client authentication, for apps that can't do SASL, reachable only from allowed_source_cidrs and firewall peers. Null on RabbitMQ. Changes in place.", Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Whether the listener is on (false when left out at create)."},
			"access":  schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "readWrite or readOnly (readOnly when left out at create)."},
		}},
		"settings": schema.SingleNestedAttribute{Optional: true, Computed: true, PlanModifiers: keepObject, Description: "Kafka's cluster settings; null on RabbitMQ, which takes none. A setting left out keeps its current value. Changes in place.", Attributes: map[string]schema.Attribute{
			"auto_create_topics":   schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Whether a topic a client names is created with the cluster's defaults (true when left out at create)."},
			"default_retention_ms": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: "How long a topic keeps messages unless it says otherwise: -1 keeps them, else 60000 to a year (a week when left out at create)."},
			"default_partitions":   schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt, Description: "Partitions of an auto-created topic, 1 to 100 (1 when left out at create)."},
		}},
		"endpoint_hostname":             computed("The name clients connect to (Kafka's bootstrap name); null until it resolves. It stays the same through node replacements, growth and upgrades."),
		"endpoint_port":                 schema.Int64Attribute{Computed: true, Description: "The TLS port: Kafka 9093 (SASL_SSL), RabbitMQ 5671 (AMQPS).", PlanModifiers: keepInt},
		"endpoint_plaintext_port":       schema.Int64Attribute{Computed: true, Description: "The port without TLS (Kafka 9092, RabbitMQ 5672) while plaintext_port is true; null otherwise."},
		"endpoint_unauthenticated_port": schema.Int64Attribute{Computed: true, Description: "Kafka's port without client authentication, 9091, while unauthenticated_access is enabled; null otherwise."},
		"broker_hostnames":              schema.ListAttribute{Computed: true, ElementType: types.StringType, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, Description: "Kafka: the name each broker advertises, by broker index. Empty on RabbitMQ."},
		"maintenance_window_utc":        schema.StringAttribute{Optional: true, Computed: true, Description: "Weekly maintenance window, ddd:HH:MM-ddd:HH:MM UTC.", PlanModifiers: keepString},
		"deletion_protection":           schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "While true, the cluster can't be destroyed."},
		"status":                        schema.StringAttribute{Computed: true, Description: "What the cluster is doing."},
		"operation_id":                  schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout":             schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultTimeout), Description: "Maximum wait for each asynchronous operation, for example 60m."},
	}}
}
func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// compareVersions orders "4.3" < "4.4" < "4.10", each dotted part as a number.
func compareVersions(a, b string) int {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(x), len(y)) {
		var p, q int
		if i < len(x) {
			p, _ = strconv.Atoi(x[i])
		}
		if i < len(y) {
			q, _ = strconv.Atoi(y[i])
		}
		if p != q {
			return p - q
		}
	}
	return 0
}

func (r *clusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(plan.Engine) && plan.Engine.ValueString() == "rabbitmq" && !req.Config.Raw.IsNull() {
		var unauth, settings types.Object
		var raise types.Bool
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("unauthenticated_access"), &unauth)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("settings"), &settings)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("raise_app_topics"), &raise)...)
		for name, configured := range map[string]attr.Value{"unauthenticated_access": unauth, "settings": settings, "raise_app_topics": raise} {
			if !configured.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root(name), name+" is Kafka's", "RabbitMQ clusters take a password on every port and have no cluster settings: leave "+name+" out.")
			}
		}
	}
	// A replacement makes a new cluster: an older version, a single node or less storage is fine then.
	if req.State.Raw.IsNull() || len(resp.RequiresReplace) > 0 {
		return
	}
	var state clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(plan.EngineVersion) && compareVersions(plan.EngineVersion.ValueString(), state.EngineVersion.ValueString()) < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("engine_version"), "engine_version can't go back",
			fmt.Sprintf("Upgrades are one-way: %s can't move to the older %s.", state.EngineVersion.ValueString(), plan.EngineVersion.ValueString()))
	}
	if known(plan.StorageSizeGiB) && known(state.StorageSizeGiB) && plan.StorageSizeGiB.ValueInt64() < state.StorageSizeGiB.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("storage_size_gib"), "Storage can't shrink", fmt.Sprintf("storage_size_gib must be at least %d.", state.StorageSizeGiB.ValueInt64()))
	}
	if known(plan.Mode) && !plan.Mode.Equal(state.Mode) {
		if plan.Mode.ValueString() != "replicated" {
			resp.Diagnostics.AddAttributeError(path.Root("mode"), "A replicated cluster stays replicated", "A cluster grows from single to replicated only; it can't go back to a single node.")
			return
		}
		// Growth adds brokers, and the plaintext and unauthenticated ports are re-read anyway.
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("broker_hostnames"), types.ListUnknown(types.StringType))...)
	}
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	body, d := createBody(ctx, m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "clusters"), body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.ID = types.StringValue(id)
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	m.Status = types.StringValue("creating")
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = waitResponse(ctx, r.client, out); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			resp.Diagnostics.AddWarning("Cluster still being created", fmt.Sprintf("Cluster %s was still being created when the wait ended. The next refresh or apply waits for operation %s.", id, m.OperationID.ValueString()))
			return
		}
		apiError(&resp.Diagnostics, fmt.Errorf("cluster %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

// knownFields are an object's known attributes under their API names.
func knownFields(o types.Object, names map[string]string) map[string]any {
	out := map[string]any{}
	if !known(o) {
		return out
	}
	for k, v := range o.Attributes() {
		if !known(v) {
			continue
		}
		switch t := v.(type) {
		case types.Bool:
			out[names[k]] = t.ValueBool()
		case types.Int64:
			out[names[k]] = t.ValueInt64()
		case types.String:
			out[names[k]] = t.ValueString()
		}
	}
	return out
}

var (
	unauthNames   = map[string]string{"enabled": "enabled", "access": "access"}
	settingsNames = map[string]string{"auto_create_topics": "autoCreateTopics", "default_retention_ms": "defaultRetentionMs", "default_partitions": "defaultPartitions"}
)

// changedFields are the attributes of o whose values differ from old's, under their API names.
func changedFields(o, old types.Object, names map[string]string) map[string]any {
	now, was := knownFields(o, names), knownFields(old, names)
	for k, v := range now {
		if was[k] == v {
			delete(now, k)
		}
	}
	return now
}

// createBody is POST /clusters': settings and unauthenticated access are Kafka's, and what's unset
// is left to the API.
func createBody(ctx context.Context, m clusterModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	tags, td := stringMap(ctx, m.Tags)
	d.Append(td...)
	storage := map[string]any{"sizeGiB": m.StorageSizeGiB.ValueInt64()}
	setInt(storage, "iops", m.StorageIOPS)
	setInt(storage, "throughputMBps", m.StorageThroughputMBps)
	body := map[string]any{"name": m.Name.ValueString(), "tags": tags, "engine": m.Engine.ValueString(), "engineVersion": m.EngineVersion.ValueString(),
		"mode": m.Mode.ValueString(), "instanceType": m.InstanceType.ValueString(), "storage": storage, "subnet": m.SubnetID.ValueString(),
		"plaintextPort": m.PlaintextPort.ValueBool(), "deletionProtection": m.DeletionProtection.ValueBool()}
	setString(body, "description", m.Description)
	if known(m.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	if known(m.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if s := knownFields(m.Settings, settingsNames); len(s) > 0 {
		body["settings"] = s
	}
	if u := knownFields(m.UnauthenticatedAccess, unauthNames); len(u) > 0 {
		body["unauthenticatedAccess"] = u
	}
	return body, d
}

// updateBody is the PATCH for everything but growth and an upgrade, which the API takes on their
// own: only what changed.
func updateBody(ctx context.Context, m, old clusterModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{}
	if !m.Name.Equal(old.Name) {
		body["name"] = m.Name.ValueString()
	}
	if !m.Description.Equal(old.Description) {
		body["description"] = m.Description.ValueStringPointer()
	}
	if !m.Tags.Equal(old.Tags) {
		tags, td := stringMap(ctx, m.Tags)
		d.Append(td...)
		body["tags"] = tags
	}
	if !m.DeletionProtection.Equal(old.DeletionProtection) {
		body["deletionProtection"] = m.DeletionProtection.ValueBool()
	}
	if !m.InstanceType.Equal(old.InstanceType) {
		body["instanceType"] = m.InstanceType.ValueString()
	}
	storage := map[string]any{}
	if !m.StorageSizeGiB.Equal(old.StorageSizeGiB) {
		storage["sizeGiB"] = m.StorageSizeGiB.ValueInt64()
	}
	if known(m.StorageIOPS) && !m.StorageIOPS.Equal(old.StorageIOPS) {
		storage["iops"] = m.StorageIOPS.ValueInt64()
	}
	if known(m.StorageThroughputMBps) && !m.StorageThroughputMBps.Equal(old.StorageThroughputMBps) {
		storage["throughputMBps"] = m.StorageThroughputMBps.ValueInt64()
	}
	if len(storage) > 0 {
		body["storage"] = storage
	}
	if known(m.AllowedSourceCIDRs) && !m.AllowedSourceCIDRs.Equal(old.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.PlaintextPort) && !m.PlaintextPort.Equal(old.PlaintextPort) {
		body["plaintextPort"] = m.PlaintextPort.ValueBool()
	}
	if known(m.MaintenanceWindowUTC) && !m.MaintenanceWindowUTC.Equal(old.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	if s := changedFields(m.Settings, old.Settings, settingsNames); len(s) > 0 {
		body["settings"] = s
	}
	if u := changedFields(m.UnauthenticatedAccess, old.UnauthenticatedAccess, unauthNames); len(u) > 0 {
		body["unauthenticatedAccess"] = u
	}
	return body, d
}

func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readCluster(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if v.Status != "creating" {
		return
	}
	if m.OperationID.ValueString() == "" {
		resp.Diagnostics.AddError("Cluster operation in progress", "The cluster is "+v.Status+" and its operation ID is unavailable. Retry refresh after the operation completes.")
		return
	}
	waitCtx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	if err = r.client.Wait(waitCtx, service, client.OrgPath(r.client.OrganizationID, "operations", m.OperationID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.refresh(waitCtx, &m, &resp.State, &resp.Diagnostics)
}

// Update sends the settings that changed, then growth, then an upgrade, each on its own: the API
// takes mode and engineVersion apart from the cluster's other settings.
func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	body, d := updateBody(ctx, m, old)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "clusters", m.ID.ValueString())
	m.OperationID, m.Status = old.OperationID, old.Status
	patch := func(b map[string]any) bool {
		out, err := r.client.Do(ctx, service, "PATCH", p, b)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return false
		}
		m.OperationID = operationID(out)
		if err = waitResponse(ctx, r.client, out); err != nil {
			apiError(&resp.Diagnostics, fmt.Errorf("cluster %s, operation %s: %w", m.ID.ValueString(), m.OperationID.ValueString(), err))
			return false
		}
		return true
	}
	if len(body) > 0 && !patch(body) {
		return
	}
	if !m.Mode.Equal(old.Mode) {
		grow := map[string]any{"mode": m.Mode.ValueString()}
		if m.Engine.ValueString() == "kafka" {
			grow["raiseAppTopics"] = m.RaiseAppTopics.ValueBool()
		}
		if !patch(grow) {
			return
		}
	}
	if !m.EngineVersion.Equal(old.EngineVersion) && !patch(map[string]any{"engineVersion": m.EngineVersion.ValueString()}) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Cluster deletion protection enabled", "Set deletion_protection to false and apply before destroying this cluster.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "clusters", m.ID.ValueString())
	out, err := r.client.Do(ctx, service, "DELETE", p, nil)
	if deleteInProgress(err) {
		err = waitDeleting(ctx, r.client, p, err)
	}
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if !missing {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	if err = waitResponse(ctx, r.client, out); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	_, err = readCluster(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Cluster deletion incomplete", "The operation succeeded but the cluster is still returned by its API. Refresh and retry.")
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}

// ImportState takes organization_id/project_id/hqc_….
func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "hqc_") || !prefixedID.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/hqc_<12-character ID>.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "id"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
}

func (r *clusterResource) refresh(ctx context.Context, m *clusterModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readCluster(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(diags, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	diags.Append(state.Set(ctx, m)...)
}

func (f *clusterFields) fill(ctx context.Context, v clusterView, org string) {
	f.ID = types.StringValue(v.Slug)
	f.OrganizationID = types.StringValue(org)
	f.ResourceName = types.StringValue(v.ResourceName)
	f.Name = types.StringValue(v.Name)
	f.Description = types.StringPointerValue(v.Description)
	f.Tags = mapValue(ctx, v.Tags)
	f.Engine = types.StringValue(v.Engine)
	f.EngineVersion = types.StringValue(v.EngineVersion)
	f.Mode = types.StringValue(v.Mode)
	f.InstanceType = types.StringValue(v.InstanceType)
	f.StorageSizeGiB, f.StorageIOPS, f.StorageThroughputMBps = types.Int64Value(v.Storage.SizeGiB), types.Int64Value(v.Storage.IOPS), types.Int64Value(v.Storage.ThroughputMBps)
	f.VPCID = types.StringValue(v.VPC)
	f.SubnetID = types.StringValue(v.Subnet)
	f.AllowedSourceCIDRs = stringList(ctx, v.AllowedSourceCidrs)
	f.PlaintextPort = types.BoolValue(v.PlaintextPort)
	f.UnauthenticatedAccess = types.ObjectNull(unauthTypes)
	if u := v.UnauthenticatedAccess; u != nil {
		f.UnauthenticatedAccess = types.ObjectValueMust(unauthTypes, map[string]attr.Value{"enabled": types.BoolValue(u.Enabled), "access": types.StringValue(u.Access)})
	}
	f.Settings = types.ObjectNull(settingsTypes)
	if v.Engine == "kafka" {
		s := v.Settings
		f.Settings = types.ObjectValueMust(settingsTypes, map[string]attr.Value{"auto_create_topics": types.BoolPointerValue(s.AutoCreateTopics),
			"default_retention_ms": types.Int64PointerValue(s.DefaultRetentionMs), "default_partitions": types.Int64PointerValue(s.DefaultPartitions)})
	}
	f.EndpointHostname, f.EndpointPort, f.EndpointPlaintextPort, f.EndpointUnauthenticatedPort = types.StringNull(), types.Int64Null(), types.Int64Null(), types.Int64Null()
	brokers := []string{}
	if e := v.Endpoint; e != nil {
		f.EndpointHostname, f.EndpointPort = types.StringValue(e.Hostname), types.Int64Value(e.Port)
		f.EndpointPlaintextPort, f.EndpointUnauthenticatedPort = types.Int64PointerValue(e.PlaintextPort), types.Int64PointerValue(e.UnauthenticatedPort)
		slices.SortFunc(e.Brokers, func(a, b brokerView) int { return int(a.Index - b.Index) })
		for _, b := range e.Brokers {
			brokers = append(brokers, b.Hostname)
		}
	}
	f.BrokerHostnames = stringList(ctx, brokers)
	f.MaintenanceWindowUTC = types.StringValue(v.Maintenance.WindowUtc)
	f.DeletionProtection = types.BoolValue(v.DeletionProtection)
	f.Status = types.StringValue(v.Status)
}

// fromAPI reads the cluster into the resource; raise_app_topics stays as configured.
func (m *clusterModel) fromAPI(ctx context.Context, v clusterView, org string) {
	description := m.Description
	m.fill(ctx, v, org)
	if description.ValueString() == "" && known(description) && v.Description == nil {
		m.Description = description // the API shows an empty description as null
	}
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(defaultTimeout)
	}
}

func readCluster(ctx context.Context, c *client.Client, project, id string) (clusterView, error) {
	var v clusterView
	out, err := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, "clusters", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = fmt.Errorf("cluster response omitted its hqc_ identity")
	}
	return v, err
}
