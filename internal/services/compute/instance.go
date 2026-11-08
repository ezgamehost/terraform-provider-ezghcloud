package compute

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

type instanceResource struct{ client *client.Client }
type instanceModel struct {
	ID               types.String `tfsdk:"id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	ProjectID        types.String `tfsdk:"project_id"`
	Name             types.String `tfsdk:"name"`
	InstanceType     types.String `tfsdk:"instance_type"`
	Image            types.String `tfsdk:"image"`
	ResolvedImageID  types.String `tfsdk:"resolved_image_id"`
	BotID            types.String `tfsdk:"bot_id"`
	SSHKeyIDs        types.List   `tfsdk:"ssh_key_ids"`
	UserData         types.String `tfsdk:"user_data"`
	RootDevice       types.String `tfsdk:"root_device"`
	RootVolume       types.Object `tfsdk:"root_volume"`
	Volumes          types.List   `tfsdk:"volume_ids"`
	Interfaces       types.List   `tfsdk:"network_interfaces"`
	Tags             types.Map    `tfsdk:"tags"`
	Status           types.String `tfsdk:"status"`
	RootVolumeID     types.String `tfsdk:"root_volume_id"`
	InterfaceIDs     types.List   `tfsdk:"network_interface_ids"`
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type interfaceModel struct {
	Subnet      types.String `tfsdk:"subnet_id"`
	Existing    types.String `tfsdk:"network_interface_id"`
	PrivateIPv4 types.String `tfsdk:"private_ipv4_address"`
	IPv4        types.Bool   `tfsdk:"ipv4"`
	IPv6        types.Bool   `tfsdk:"ipv6"`
	Firewalls   types.List   `tfsdk:"firewall_ids"`
}
type rootVolumeModel struct {
	SizeGiB    types.Int64  `tfsdk:"size_gib"`
	VolumeType types.String `tfsdk:"volume_type"`
	IOPS       types.Int64  `tfsdk:"iops"`
	Throughput types.Int64  `tfsdk:"throughput_mbps"`
	Snapshot   types.String `tfsdk:"snapshot_id"`
}

var interfaceTypes = map[string]attr.Type{"subnet_id": types.StringType, "network_interface_id": types.StringType, "private_ipv4_address": types.StringType, "ipv4": types.BoolType, "ipv6": types.BoolType, "firewall_ids": types.ListType{ElemType: types.StringType}}
var rootVolumeTypes = map[string]attr.Type{"size_gib": types.Int64Type, "volume_type": types.StringType, "iops": types.Int64Type, "throughput_mbps": types.Int64Type, "snapshot_id": types.StringType}

type instanceView struct {
	ID, Slug, Name, InstanceType, Status, RootDevice string
	Tags                                             map[string]string
	Image                                            struct{ ID, Alias string }
	Bot                                              *struct{ ID string }
	SSHKeys                                          []struct{ ID string }
	NetworkInterfaceIDs                              []string
	RootVolumeID                                     *string
	VolumeIDs                                        []string
}

func (*instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}
func (*instanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "A compute instance. Launch topology changes replace the instance. Existing volumes and interfaces remain independently owned.", Attributes: map[string]schema.Attribute{
		"id": computedID(), "organization_id": organizationAttribute(), "project_id": projectAttribute(), "name": schema.StringAttribute{Required: true},
		"instance_type": schema.StringAttribute{Required: true, PlanModifiers: replace}, "image": schema.StringAttribute{Optional: true, Description: "Image alias or exact img_ ID. Required unless root_volume.snapshot_id is set. Aliases resolve once for each launch.", PlanModifiers: replace},
		"resolved_image_id": schema.StringAttribute{Computed: true}, "bot_id": schema.StringAttribute{Optional: true, Description: "Canonical bot_ identity attached to the instance; use ezghcloud_bot.example.slug. Updates take effect without restarting."},
		"ssh_key_ids": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})), PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
		"user_data":   schema.StringAttribute{Optional: true, Sensitive: true, Description: "Base64 user data, at most 16 KiB decoded. Terraform state stores this value. Unreadable after launch; import cannot recover it.", PlanModifiers: replace},
		"root_device": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("volume"), Description: "volume or instanceStore.", PlanModifiers: replace},
		"root_volume": schema.SingleNestedAttribute{Optional: true, Description: "Root volume launch settings. Omit with instanceStore. Import cannot recover original launch sizing without configuration.", PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()}, Attributes: map[string]schema.Attribute{
			"size_gib": schema.Int64Attribute{Optional: true}, "volume_type": schema.StringAttribute{Optional: true}, "iops": schema.Int64Attribute{Optional: true}, "throughput_mbps": schema.Int64Attribute{Optional: true}, "snapshot_id": schema.StringAttribute{Optional: true},
		}},
		"volume_ids": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})), Description: "Existing independent volumes attached at launch; never deleted on instance termination.", PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
		"network_interfaces": schema.ListNestedAttribute{Required: true, Description: "Ordered launch interfaces; index zero is primary. Omitted firewall_ids uses defaults; [] attaches none.", PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"subnet_id": schema.StringAttribute{Optional: true}, "network_interface_id": schema.StringAttribute{Optional: true}, "private_ipv4_address": schema.StringAttribute{Optional: true}, "ipv4": schema.BoolAttribute{Optional: true}, "ipv6": schema.BoolAttribute{Optional: true}, "firewall_ids": schema.ListAttribute{Optional: true, ElementType: types.StringType},
		}}},
		"tags": tagsAttribute(), "status": schema.StringAttribute{Computed: true}, "root_volume_id": schema.StringAttribute{Computed: true}, "network_interface_ids": schema.ListAttribute{Computed: true, ElementType: types.StringType}, "operation_id": schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."}, "operation_timeout": timeoutAttribute(),
	}}
}
func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req, &resp.Diagnostics, &r.client)
}
func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	body, d := instanceBody(ctx, m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if image, ok := body["image"].(string); ok {
		out, err := r.client.Do(ctx, "images", "GET", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "image-resolution")+"?ref="+url.QueryEscape(image), nil)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		var v map[string]any
		if err = out.Decode(&v); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		resolved, err := canonicalID(v)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		body["image"] = resolved
		m.ResolvedImageID = types.StringValue(resolved)
	} else {
		m.ResolvedImageID = types.StringValue("")
	}
	out, err := r.client.Do(ctx, "compute", "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "instances"), body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body, "instance")
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.ID = types.StringValue(id)
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	m.Status = types.StringValue("pending")
	m.RootVolumeID = types.StringNull()
	m.InterfaceIDs = stringsValue(ctx, nil)
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = waitResponse(ctx, r.client, "compute", out); err != nil {
		apiError(&resp.Diagnostics, fmt.Errorf("instance %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	v, err := readInstance(ctx, r.client, m.ProjectID.ValueString(), id)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readInstance(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	if m.Interfaces.IsNull() {
		ifs, e := importInterfaces(ctx, r.client, m.ProjectID.ValueString(), v.NetworkInterfaceIDs)
		if e != nil {
			apiError(&resp.Diagnostics, e)
			return
		}
		m.Interfaces = ifs
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if transitionalInstance(v.Status) {
		waitCtx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
		defer cancel()
		if resp.Diagnostics.HasError() {
			return
		}
		if m.OperationID.ValueString() == "" {
			resp.Diagnostics.AddWarning("Instance operation in progress", "The instance is transitional and its operation ID is unavailable. Retry refresh after the operation completes.")
			return
		}
		if err = r.client.Wait(waitCtx, "compute", client.OrgPath(r.client.OrganizationID, "operations", m.OperationID.ValueString())); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		v, err = readInstance(waitCtx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if transitionalInstance(v.Status) {
			resp.Diagnostics.AddWarning("Instance operation in progress", "The instance is still transitional. Retry refresh after its current operation completes.")
		}
	}
}
func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	var bot any
	if !m.BotID.IsNull() {
		bot = m.BotID.ValueString()
	}
	out, err := r.client.Do(ctx, "compute", "PATCH", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "instances", m.ID.ValueString()), map[string]any{"name": m.Name.ValueString(), "tags": tags, "bot": bot})
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	var v instanceView
	if err = out.Decode(&v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, "compute", "DELETE", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "instances", m.ID.ValueString()), nil)
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if !missing {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	if err = waitResponse(ctx, r.client, "compute", out); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	_, err = readInstance(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Instance deletion incomplete", "The operation succeeded but the instance is still returned by its API. Refresh and retry.")
		return
	}
	missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
	if !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client)
}
func (m *instanceModel) fromAPI(ctx context.Context, v instanceView, org string) {
	m.ID = types.StringValue(v.Slug)
	m.OrganizationID = types.StringValue(org)
	m.Name = types.StringValue(v.Name)
	m.InstanceType = types.StringValue(v.InstanceType)
	m.Status = types.StringValue(v.Status)
	m.Tags = mapValue(ctx, v.Tags)
	m.RootDevice = types.StringValue(v.RootDevice)
	m.RootVolumeID = nullableString(v.RootVolumeID)
	m.InterfaceIDs = stringsValue(ctx, v.NetworkInterfaceIDs)
	m.ResolvedImageID = types.StringValue(v.Image.ID)
	if m.Image.IsNull() || m.Image.IsUnknown() {
		if v.Image.ID != "" {
			m.Image = types.StringValue(v.Image.ID)
		}
	}
	if v.Bot == nil {
		m.BotID = types.StringNull()
	} else {
		m.BotID = types.StringValue(v.Bot.ID)
	}
	keys := []string{}
	for _, k := range v.SSHKeys {
		keys = append(keys, k.ID)
	}
	m.SSHKeyIDs = stringsValue(ctx, keys)
	volumes := []string{}
	for _, id := range v.VolumeIDs {
		if v.RootVolumeID == nil || id != *v.RootVolumeID {
			volumes = append(volumes, id)
		}
	}
	m.Volumes = stringsValue(ctx, volumes)
	if m.OperationID.IsNull() || m.OperationID.IsUnknown() {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue("30m")
	}
}
func readInstance(ctx context.Context, c *client.Client, p, id string) (instanceView, error) {
	var v instanceView
	out, err := c.Do(ctx, "compute", "GET", client.ProjectPath(c.OrganizationID, p, "instances", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && v.Slug == "" {
		err = fmt.Errorf("instance response omitted canonical identifier")
	}
	return v, err
}
func instanceBody(ctx context.Context, m instanceModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	tags, dd := stringMap(ctx, m.Tags)
	d.Append(dd...)
	body := map[string]any{"name": m.Name.ValueString(), "instanceType": m.InstanceType.ValueString(), "tags": tags, "rootDevice": m.RootDevice.ValueString()}
	if !m.Image.IsNull() {
		body["image"] = m.Image.ValueString()
	}
	if !m.BotID.IsNull() {
		body["bot"] = m.BotID.ValueString()
	}
	if !m.UserData.IsNull() {
		body["userData"] = m.UserData.ValueString()
	}
	var keys, volumes []string
	d.Append(m.SSHKeyIDs.ElementsAs(ctx, &keys, false)...)
	d.Append(m.Volumes.ElementsAs(ctx, &volumes, false)...)
	body["sshKeyIds"] = keys
	vs := []map[string]any{}
	for _, id := range volumes {
		vs = append(vs, map[string]any{"volumeId": id})
	}
	body["volumes"] = vs
	if !m.RootVolume.IsNull() {
		var root rootVolumeModel
		d.Append(m.RootVolume.As(ctx, &root, basetypes.ObjectAsOptions{})...)
		v := map[string]any{}
		if !root.SizeGiB.IsNull() {
			v["sizeGiB"] = root.SizeGiB.ValueInt64()
		}
		if !root.VolumeType.IsNull() {
			v["volumeType"] = root.VolumeType.ValueString()
		}
		if !root.IOPS.IsNull() {
			v["iops"] = root.IOPS.ValueInt64()
		}
		if !root.Throughput.IsNull() {
			v["throughputMBps"] = root.Throughput.ValueInt64()
		}
		if !root.Snapshot.IsNull() {
			v["snapshotId"] = root.Snapshot.ValueString()
		}
		body["rootVolume"] = v
	}
	var interfaces []interfaceModel
	d.Append(m.Interfaces.ElementsAs(ctx, &interfaces, false)...)
	if len(interfaces) == 0 {
		d.AddError("Missing network interfaces", "An instance requires at least one network_interfaces entry.")
	}
	ifs := []map[string]any{}
	for _, i := range interfaces {
		v := map[string]any{}
		if !i.Existing.IsNull() {
			v["networkInterfaceId"] = i.Existing.ValueString()
		}
		if !i.Subnet.IsNull() {
			v["subnet"] = i.Subnet.ValueString()
		}
		if !i.PrivateIPv4.IsNull() {
			v["privateIpv4Address"] = i.PrivateIPv4.ValueString()
		}
		if !i.IPv4.IsNull() {
			v["ipv4"] = i.IPv4.ValueBool()
		}
		if !i.IPv6.IsNull() {
			v["ipv6"] = i.IPv6.ValueBool()
		}
		if !i.Firewalls.IsNull() {
			var f []string
			d.Append(i.Firewalls.ElementsAs(ctx, &f, false)...)
			if f == nil {
				f = []string{}
			}
			v["firewallIds"] = f
		}
		ifs = append(ifs, v)
	}
	body["networkInterfaces"] = ifs
	return body, d
}

func transitionalInstance(s string) bool {
	switch s {
	case "pending", "starting", "stopping", "rebooting", "resizing", "terminating", "recovering":
		return true
	}
	return false
}
