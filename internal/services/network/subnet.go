package network

import (
	"context"
	"fmt"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type subnetResource struct{ client *client.Client }
type subnetModel struct {
	ID                   types.String `tfsdk:"id"`
	OrganizationID       types.String `tfsdk:"organization_id"`
	ProjectID            types.String `tfsdk:"project_id"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	Tags                 types.Map    `tfsdk:"tags"`
	ResourceName         types.String `tfsdk:"resource_name"`
	Status               types.String `tfsdk:"status"`
	IPv4CIDR             types.String `tfsdk:"ipv4_cidr"`
	IPv6CIDR             types.String `tfsdk:"ipv6_cidr"`
	VPCID                types.String `tfsdk:"vpc_id"`
	AutoAssignPublicIPv4 types.Bool   `tfsdk:"auto_assign_public_ipv4"`
	AutoAssignPublicIPv6 types.Bool   `tfsdk:"auto_assign_public_ipv6"`
	RouteTableID         types.String `tfsdk:"route_table_id"`
}
type subnetWire struct {
	ID                   string            `json:"id"`
	Slug                 string            `json:"slug"`
	Name                 string            `json:"name"`
	Description          *string           `json:"description"`
	Tags                 map[string]string `json:"tags"`
	ResourceName         string            `json:"resourceName"`
	Status               string            `json:"status"`
	IPv4CIDR             string            `json:"ipv4Cidr"`
	IPv6CIDR             string            `json:"ipv6Cidr"`
	VPCID                string            `json:"vpc"`
	AutoAssignPublicIPv4 bool              `json:"autoAssignPublicIpv4"`
	AutoAssignPublicIPv6 bool              `json:"autoAssignPublicIpv6"`
	RouteTable           string            `json:"routeTable"`
}

func (m *subnetModel) fromWire(ctx context.Context, v subnetWire) {
	m.ID = types.StringValue(v.Slug)
	m.Name = types.StringValue(v.Name)
	m.Description = types.StringPointerValue(v.Description)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.ResourceName = types.StringValue(v.ResourceName)
	m.Status = types.StringValue(v.Status)
	m.IPv4CIDR = types.StringValue(v.IPv4CIDR)
	m.IPv6CIDR = types.StringValue(v.IPv6CIDR)
	m.VPCID = types.StringValue(v.VPCID)
	m.AutoAssignPublicIPv4 = types.BoolValue(v.AutoAssignPublicIPv4)
	m.AutoAssignPublicIPv6 = types.BoolValue(v.AutoAssignPublicIPv6)
	// The effective table: the subnet's own, else the VPC's main one.
	m.RouteTableID = types.StringNull()
	if v.RouteTable != "" {
		m.RouteTableID = types.StringValue(v.RouteTable)
	}
}
func (r *subnetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subnet"
}
func (r *subnetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := baseAttributes()
	a["ipv4_cidr"] = schema.StringAttribute{Required: true, Description: "Canonical IPv4 address space; changes replace the resource.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	a["ipv6_cidr"] = schema.StringAttribute{Computed: true, Description: "Service-assigned IPv6 address space."}
	a["vpc_id"] = schema.StringAttribute{Required: true, Description: "Parent VPC ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	a["auto_assign_public_ipv4"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Default public IPv4 allocation for new interfaces."}
	a["auto_assign_public_ipv6"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Default public IPv6 allocation for new interfaces."}
	a["route_table_id"] = schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		Description: "Route table (rtb_) the subnet uses. Set it to associate the subnet with a table of its VPC. Omitted, it reports the table in use (the VPC's main table until the subnet is associated with another) and nothing is changed; removing it from the configuration likewise leaves the subnet on its current table. To return a subnet to the main table, set this to the main table's ID."}
	resp.Schema = schema.Schema{Description: "Manage an EZGH Cloud subnet.", Attributes: a}
}
func (r *subnetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", fmt.Sprintf("Expected API client, got %T", req.ProviderData))
	}
}
func (r *subnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m subnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	var tags map[string]string
	resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags, "ipv4Cidr": m.IPv4CIDR.ValueString()}
	body["vpc"] = m.VPCID.ValueString()
	response, err := r.client.Do(ctx, "network", "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "subnets"), body)
	if err != nil {
		resp.Diagnostics.AddError("Create subnet", err.Error())
		return
	}
	var v subnetWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode subnet", err.Error())
		return
	}
	planned4, planned6, table := m.AutoAssignPublicIPv4, m.AutoAssignPublicIPv6, m.RouteTableID
	m.fromWire(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	m.AutoAssignPublicIPv4, m.AutoAssignPublicIPv6 = planned4, planned6
	patch := map[string]any{}
	if m.AutoAssignPublicIPv4.ValueBool() || m.AutoAssignPublicIPv6.ValueBool() {
		patch["autoAssignPublicIpv4"], patch["autoAssignPublicIpv6"] = m.AutoAssignPublicIPv4.ValueBool(), m.AutoAssignPublicIPv6.ValueBool()
	}
	// Unknown when the configuration names no table: the subnet stays on the VPC's main one.
	if !table.IsUnknown() && !table.IsNull() {
		patch["routeTable"] = table.ValueString()
	}
	if len(patch) > 0 {
		// Launch defaults and the route table are patched after the create endpoint assigns its ID.
		var updated subnetWire
		response, err = r.client.Do(ctx, "network", "PATCH", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "subnets", m.ID.ValueString()), patch)
		if err != nil {
			resp.Diagnostics.AddError("Set subnet launch defaults and route table", err.Error())
			return
		}
		if err = response.Decode(&updated); err != nil {
			resp.Diagnostics.AddError("Decode subnet", err.Error())
			return
		}
		m.fromWire(ctx, updated)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}
func (r *subnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m subnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "network", "GET", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "subnets", m.ID.ValueString()), nil)
	if err != nil {
		gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err)
		if gone {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read subnet", e.Error())
		return
	}
	var v subnetWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode subnet", err.Error())
		return
	}
	m.fromWire(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *subnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior subnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	var tags map[string]string
	resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags}
	body["autoAssignPublicIpv4"] = m.AutoAssignPublicIPv4.ValueBool()
	body["autoAssignPublicIpv6"] = m.AutoAssignPublicIPv6.ValueBool()
	// Only a configured change of table is sent: an omitted route_table_id never re-associates.
	if !m.RouteTableID.IsUnknown() && !m.RouteTableID.IsNull() && !m.RouteTableID.Equal(prior.RouteTableID) {
		body["routeTable"] = m.RouteTableID.ValueString()
	}
	response, err := r.client.Do(ctx, "network", "PATCH", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "subnets", m.ID.ValueString()), body)
	if err != nil {
		resp.Diagnostics.AddError("Update subnet", err.Error())
		return
	}
	var v subnetWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode subnet", err.Error())
		return
	}
	m.fromWire(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *subnetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m subnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	if err := deleteResource(ctx, r.client, "network", m.ProjectID.ValueString(), "subnets", m.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete subnet", err.Error())
	}
}
func (r *subnetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, project, id, err := parseImport(req.ID, "sbn_")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if r.client != nil && org != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the provider organization.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), org)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
}
