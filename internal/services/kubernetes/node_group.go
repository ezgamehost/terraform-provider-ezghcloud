package kubernetes

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const nodeGroupTimeout = "30m"

type taint struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

var taintType = types.ObjectType{AttrTypes: map[string]attr.Type{"key": types.StringType, "value": types.StringType, "effect": types.StringType}}

type nodeGroupModel struct {
	ID                    types.String `tfsdk:"id"`
	OrganizationID        types.String `tfsdk:"organization_id"`
	ProjectID             types.String `tfsdk:"project_id"`
	ClusterID             types.String `tfsdk:"cluster_id"`
	ResourceName          types.String `tfsdk:"resource_name"`
	Name                  types.String `tfsdk:"name"`
	Version               types.String `tfsdk:"version"`
	InstanceType          types.String `tfsdk:"instance_type"`
	SubnetID              types.String `tfsdk:"subnet_id"`
	RootVolumeSizeGiB     types.Int64  `tfsdk:"root_volume_size_gib"`
	MinSize               types.Int64  `tfsdk:"min_size"`
	MaxSize               types.Int64  `tfsdk:"max_size"`
	DesiredSize           types.Int64  `tfsdk:"desired_size"`
	Labels                types.Map    `tfsdk:"labels"`
	Taints                types.List   `tfsdk:"taints"`
	SSHKeyIDs             types.List   `tfsdk:"ssh_key_ids"`
	BotID                 types.String `tfsdk:"bot_id"`
	MaxUnavailablePercent types.Int64  `tfsdk:"max_unavailable_percent"`
	AutoscalingGroupID    types.String `tfsdk:"autoscaling_group_id"`
	Tags                  types.Map    `tfsdk:"tags"`
	Status                types.String `tfsdk:"status"`
	OperationID           types.String `tfsdk:"operation_id"`
	OperationTimeout      types.String `tfsdk:"operation_timeout"`
}
type nodeGroupView struct {
	Slug, ResourceName, Name, Cluster, Status, Version, InstanceType, Subnet, Bot string
	RootVolumeSizeGiB, MinSize, MaxSize, DesiredSize                              int64
	Labels, Tags                                                                  map[string]string
	Taints                                                                        []struct{ Key, Value, Effect string }
	SshKeyIds                                                                     []string
	UpdateConfig                                                                  struct{ MaxUnavailablePercent int64 }
	AutoscalingGroup                                                              *string
}

type nodeGroupResource struct{ client *client.Client }

func (*nodeGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_group"
}

func (*nodeGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{Description: "A node group: a cluster's nodes, instances of one type on one autoscaling group. Cluster Autoscaler sizes it between min_size and max_size. Up to 100 nodes, 110 pods per node.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "Node group ID (kng_).", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "The cluster's project. Changing it replaces the node group.", PlanModifiers: replace},
		"cluster_id":      schema.StringAttribute{Required: true, Description: "Cluster (kcl_). Changing it replaces the node group.", PlanModifiers: replace},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"name":            schema.StringAttribute{Required: true, Description: "Node group name. Changing it replaces the node group.", PlanModifiers: replace},
		"version":         schema.StringAttribute{Optional: true, Computed: true, Description: "Kubernetes minor version of the nodes, at most the cluster's. Left out, the cluster's. Changing it replaces the nodes a few at a time, each drained first.", PlanModifiers: keepString},
		"instance_type":   schema.StringAttribute{Required: true, Description: "Instance type of the nodes. Changing it replaces the node group.", PlanModifiers: replace},
		"subnet_id":       schema.StringAttribute{Required: true, Description: "Private subnet (sbn_) in the cluster's VPC. Changing it replaces the node group.", PlanModifiers: replace},
		"root_volume_size_gib": schema.Int64Attribute{Optional: true, Computed: true, Description: "Root volume size of each node in GiB. Changing it replaces the node group.",
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()}},
		"min_size":     schema.Int64Attribute{Required: true, Description: "Fewest nodes, 0 to 100."},
		"max_size":     schema.Int64Attribute{Required: true, Description: "Most nodes, 1 to 100."},
		"desired_size": schema.Int64Attribute{Optional: true, Computed: true, Description: "Nodes to start with. Left out, min_size. Only applies at create: Cluster Autoscaler owns the size after that, so it's never drift.", PlanModifiers: keepInt},
		"labels":       schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Kubernetes labels on each node."},
		"taints": schema.ListNestedAttribute{Optional: true, Computed: true, Default: listdefault.StaticValue(types.ListValueMust(taintType, []attr.Value{})), Description: "Kubernetes taints on each node.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"key":    schema.StringAttribute{Required: true, Description: "Taint key."},
				"value":  schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Taint value; empty for none."},
				"effect": schema.StringAttribute{Required: true, Description: "NoSchedule, PreferNoSchedule or NoExecute."},
			}}},
		"ssh_key_ids": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})), Description: "SSH keys (ssh_) on each node, at most 10. Changing them replaces the node group.",
			PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
		"bot_id":                  schema.StringAttribute{Required: true, Description: "Node Bot (bot_), the nodes' Instance Bot. Needs KubernetesNodeAccess, and the cluster's Bot needs bots.pass on it. Changing it replaces the node group.", PlanModifiers: replace},
		"max_unavailable_percent": schema.Int64Attribute{Optional: true, Computed: true, Description: "Most nodes replaced at once during an upgrade, as a percentage. Left out, 25.", PlanModifiers: keepInt},
		"autoscaling_group_id":    schema.StringAttribute{Computed: true, Description: "The node group's autoscaling group (asg_). Changes made to it directly are replaced by the node group.", PlanModifiers: keepString},
		"tags":                    schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"status":                  schema.StringAttribute{Computed: true, Description: "What the node group is doing: creating, active, updating, deleting or failed."},
		"operation_id":            schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(nodeGroupTimeout), Description: "Maximum wait for each asynchronous operation, for example 30m."},
	}}
}
func (r *nodeGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *nodeGroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state nodeGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if !resp.Diagnostics.HasError() && known(plan.DesiredSize) && !plan.DesiredSize.Equal(state.DesiredSize) {
		resp.Diagnostics.AddAttributeWarning(path.Root("desired_size"), "desired_size only applies at create",
			"Cluster Autoscaler sizes the node group after it's created. This change is saved to state and does nothing; change min_size or max_size instead.")
	}
}

func (r *nodeGroupResource) path(m nodeGroupModel, id ...string) string {
	return clusterPath(r.client, m.ProjectID.ValueString(), m.ClusterID.ValueString(), append([]string{"node-groups"}, id...)...)
}

func (r *nodeGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m nodeGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, nodeGroupTimeout, &resp.Diagnostics)
	defer cancel()
	body := map[string]any{"name": m.Name.ValueString(), "instanceType": m.InstanceType.ValueString(), "subnet": m.SubnetID.ValueString(),
		"minSize": m.MinSize.ValueInt64(), "maxSize": m.MaxSize.ValueInt64(), "bot": m.BotID.ValueString(),
		"labels": stringMap(ctx, m.Labels, &resp.Diagnostics), "taints": taintsBody(ctx, m.Taints, &resp.Diagnostics),
		"sshKeyIds": stringsOf(ctx, m.SSHKeyIDs, &resp.Diagnostics), "tags": stringMap(ctx, m.Tags, &resp.Diagnostics)}
	if known(m.Version) {
		body["version"] = m.Version.ValueString()
	}
	if known(m.RootVolumeSizeGiB) {
		body["rootVolumeSizeGiB"] = m.RootVolumeSizeGiB.ValueInt64()
	}
	if known(m.DesiredSize) {
		body["desiredSize"] = m.DesiredSize.ValueInt64()
	}
	if known(m.MaxUnavailablePercent) {
		body["updateConfig"] = map[string]any{"maxUnavailablePercent": m.MaxUnavailablePercent.ValueInt64()}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID, m.Status = types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	if !startAsync(ctx, r.client, r.path(m), body, &m.ID, &m.OperationID, &resp.State, &m, "Node group", &resp.Diagnostics) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func taintsBody(ctx context.Context, v types.List, d *diag.Diagnostics) []map[string]any {
	var ts []taint
	if known(v) {
		d.Append(v.ElementsAs(ctx, &ts, false)...)
	}
	out := []map[string]any{}
	for _, t := range ts {
		out = append(out, map[string]any{"key": t.Key.ValueString(), "value": t.Value.ValueString(), "effect": t.Effect.ValueString()})
	}
	return out
}

func (r *nodeGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m nodeGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v nodeGroupView
	if readOrForget(ctx, r.client, m.ProjectID.ValueString(), r.path(m, m.ID.ValueString()), "kng_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *nodeGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old nodeGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, nodeGroupTimeout, &resp.Diagnostics)
	defer cancel()
	body := map[string]any{}
	if known(m.Version) && !m.Version.Equal(old.Version) {
		body["version"] = m.Version.ValueString()
	}
	if !m.MinSize.Equal(old.MinSize) {
		body["minSize"] = m.MinSize.ValueInt64()
	}
	if !m.MaxSize.Equal(old.MaxSize) {
		body["maxSize"] = m.MaxSize.ValueInt64()
	}
	if !m.Labels.Equal(old.Labels) {
		body["labels"] = stringMap(ctx, m.Labels, &resp.Diagnostics)
	}
	if !m.Taints.Equal(old.Taints) {
		body["taints"] = taintsBody(ctx, m.Taints, &resp.Diagnostics)
	}
	if known(m.MaxUnavailablePercent) && !m.MaxUnavailablePercent.Equal(old.MaxUnavailablePercent) {
		body["updateConfig"] = map[string]any{"maxUnavailablePercent": m.MaxUnavailablePercent.ValueInt64()}
	}
	if !m.Tags.Equal(old.Tags) {
		body["tags"] = stringMap(ctx, m.Tags, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if len(body) == 0 { // desired_size or provider settings only
		m.OperationID = old.OperationID
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", r.path(m, m.ID.ValueString()), body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if finish(ctx, r.client, out, m.ID.ValueString(), m.OperationID.ValueString(), "Node group", &resp.Diagnostics) {
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
	}
}

func (r *nodeGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m nodeGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, nodeGroupTimeout, &resp.Diagnostics)
	defer cancel()
	if err := remove(ctx, r.client, r.path(m, m.ID.ValueString())); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *nodeGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(ctx, req, resp, r.client, []string{"cluster_id", "id"}, []string{"kcl_", "kng_"})
}

func (r *nodeGroupResource) refresh(ctx context.Context, m *nodeGroupModel, state *tfsdk.State, d *diag.Diagnostics) {
	var v nodeGroupView
	if err := get(ctx, r.client, r.path(*m, m.ID.ValueString()), "kng_", &v); err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	d.Append(state.Set(ctx, m)...)
}

// fromAPI reads the node group into m. desired_size keeps its configured value: Cluster
// Autoscaler moves the real one, which is never drift. An import reads it.
func (m *nodeGroupModel) fromAPI(ctx context.Context, v nodeGroupView, org string) {
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.ClusterID, m.Name, m.Version = types.StringValue(v.Cluster), types.StringValue(v.Name), types.StringValue(v.Version)
	m.InstanceType, m.SubnetID, m.BotID = types.StringValue(v.InstanceType), types.StringValue(v.Subnet), types.StringValue(v.Bot)
	m.RootVolumeSizeGiB, m.MinSize, m.MaxSize = types.Int64Value(v.RootVolumeSizeGiB), types.Int64Value(v.MinSize), types.Int64Value(v.MaxSize)
	if !known(m.DesiredSize) {
		m.DesiredSize = types.Int64Value(v.DesiredSize)
	}
	m.Labels, m.Tags, m.SSHKeyIDs = mapValue(ctx, v.Labels), mapValue(ctx, v.Tags), listValue(ctx, v.SshKeyIds)
	ts := []taint{}
	for _, t := range v.Taints {
		ts = append(ts, taint{Key: types.StringValue(t.Key), Value: types.StringValue(t.Value), Effect: types.StringValue(t.Effect)})
	}
	m.Taints, _ = types.ListValueFrom(ctx, taintType, ts)
	m.MaxUnavailablePercent = types.Int64Value(v.UpdateConfig.MaxUnavailablePercent)
	m.AutoscalingGroupID, m.Status = types.StringPointerValue(v.AutoscalingGroup), types.StringValue(v.Status)
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(nodeGroupTimeout)
	}
}
