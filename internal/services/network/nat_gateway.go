package network

import (
	"context"
	"fmt"
	"os"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// natPreview is whether NAT Gateways, not launched yet, are served: only with
// EZGHCLOUD_PREVIEW_NAT_GATEWAY=1 (the tests set it), so releases and their Registry docs leave
// the resource out. At launch, drop it, add the README rows and run make docs.
func natPreview() bool { return os.Getenv("EZGHCLOUD_PREVIEW_NAT_GATEWAY") == "1" }

type natGatewayResource struct{ client *client.Client }
type natGatewayModel struct {
	ID                  types.String `tfsdk:"id"`
	OrganizationID      types.String `tfsdk:"organization_id"`
	ProjectID           types.String `tfsdk:"project_id"`
	Name                types.String `tfsdk:"name"`
	VPCID               types.String `tfsdk:"vpc_id"`
	SubnetID            types.String `tfsdk:"subnet_id"`
	PublicIPv4Addresses types.List   `tfsdk:"public_ipv4_addresses"`
	Status              types.String `tfsdk:"status"`
}
type natAddressWire struct {
	ID      string `json:"id" tfsdk:"id"`
	Address string `json:"address" tfsdk:"address"`
}
type natGatewayWire struct {
	Slug                string           `json:"slug"`
	Name                string           `json:"name"`
	VPC                 string           `json:"vpc"`
	Subnet              string           `json:"subnet"`
	PublicIPv4Addresses []natAddressWire `json:"publicIpv4Addresses"`
	Status              string           `json:"status"`
}

var natAddressType = types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "address": types.StringType}}

func (m *natGatewayModel) fromWire(ctx context.Context, v natGatewayWire) diag.Diagnostics {
	m.ID = types.StringValue(v.Slug)
	m.Name = types.StringValue(v.Name)
	m.VPCID = types.StringValue(v.VPC)
	m.SubnetID = types.StringValue(v.Subnet)
	m.Status = types.StringValue(v.Status)
	if v.PublicIPv4Addresses == nil {
		v.PublicIPv4Addresses = []natAddressWire{}
	}
	var diags diag.Diagnostics
	m.PublicIPv4Addresses, diags = types.ListValueFrom(ctx, natAddressType, v.PublicIPv4Addresses)
	return diags
}

func (r *natGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nat_gateway"
}
func (r *natGatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep, replace := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manage an EZGH Cloud NAT gateway: outbound IPv4 internet access for a VPC's private subnets behind one public IPv4 address. Creating a gateway changes no routes; a subnet uses it once its route table has a route to it.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, Description: "Canonical prefixed resource ID (nat_).", PlanModifiers: keep},
			"organization_id": schema.StringAttribute{Computed: true, Description: "Organization owning this resource.", PlanModifiers: keep},
			"project_id":      schema.StringAttribute{Required: true, Description: "Project owning this resource.", PlanModifiers: replace},
			"name":            schema.StringAttribute{Required: true, Description: "Resource name."},
			"vpc_id":          schema.StringAttribute{Required: true, Description: "VPC (vpc_) the gateway serves. It needs an attached Network Gateway. Changing it replaces the gateway and its public address.", PlanModifiers: replace},
			"subnet_id":       schema.StringAttribute{Required: true, Description: "Public subnet (sbn_) of the VPC the gateway is in: its route table routes 0.0.0.0/0 to the VPC's Network Gateway. Keep 4 addresses free in it. Changing it replaces the gateway and its public address.", PlanModifiers: replace},
			"public_ipv4_addresses": schema.ListNestedAttribute{Computed: true, Description: "Public IPv4 addresses the gateway's traffic leaves from. Assigned at create; they don't change for the life of the gateway.",
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":      schema.StringAttribute{Computed: true, Description: "Public IPv4 address ID (pip_)."},
					"address": schema.StringAttribute{Computed: true, Description: "The IPv4 address."},
				}}},
			"status": schema.StringAttribute{Computed: true, Description: "Current service status."},
		},
	}
}
func (r *natGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", fmt.Sprintf("Expected API client, got %T", req.ProviderData))
	}
}

func (r *natGatewayResource) path(m natGatewayModel, id ...string) string {
	return client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "nat-gateways", id...)
}

// decode sets m from a response; a response without the gateway's ID never replaces the state.
func (m *natGatewayModel) decode(ctx context.Context, response *client.Response) diag.Diagnostics {
	var v natGatewayWire
	var diags diag.Diagnostics
	if err := response.Decode(&v); err != nil {
		diags.AddError("Decode NAT gateway", err.Error())
		return diags
	}
	if v.Slug == "" {
		diags.AddError("Decode NAT gateway", "API returned a NAT gateway without an ID")
		return diags
	}
	return m.fromWire(ctx, v)
}

func (r *natGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	body := map[string]any{"name": m.Name.ValueString(), "vpc": m.VPCID.ValueString(), "subnet": m.SubnetID.ValueString()}
	accepted, err := r.client.Do(ctx, "network", "POST", r.path(m), body)
	if err != nil {
		resp.Diagnostics.AddError("Create NAT gateway", err.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, accepted)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The gateway exists from the accepted answer on: it's in state before the wait, so a failed
	// or interrupted wait leaves it tracked rather than orphaned.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if err = r.client.Wait(ctx, "network", accepted.Header.Get("Location")); err != nil {
		resp.Diagnostics.AddError("Create NAT gateway", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "network", "GET", r.path(m, m.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Read NAT gateway", err.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *natGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "network", "GET", r.path(m, m.ID.ValueString()), nil)
	if err != nil {
		gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err)
		if gone {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read NAT gateway", e.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Status.ValueString() == "deleted" {
		resp.State.RemoveResource(ctx)
		return
	}
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *natGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "network", "PATCH", r.path(m, m.ID.ValueString()), map[string]any{"name": m.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Update NAT gateway", err.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *natGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	accepted, err := r.client.Do(ctx, "network", "DELETE", r.path(m, m.ID.ValueString()), nil)
	if err != nil {
		if gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err); !gone {
			resp.Diagnostics.AddError("Delete NAT gateway", e.Error())
		}
		return
	}
	// The delete is done, and its public address released, only once its operation has succeeded.
	if err = r.client.Wait(ctx, "network", accepted.Header.Get("Location")); err != nil {
		resp.Diagnostics.AddError("Delete NAT gateway", err.Error())
	}
}
func (r *natGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, project, id, err := parseImport(req.ID, "nat_")
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
