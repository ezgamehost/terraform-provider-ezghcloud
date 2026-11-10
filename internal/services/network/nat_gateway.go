package network

import (
	"context"
	"fmt"
	"os"
	"time"

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
	ResourceName        types.String `tfsdk:"resource_name"`
	Timeouts            types.Object `tfsdk:"timeouts"`
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
	ResourceName        string           `json:"resourceName"`
	PublicIPv4Addresses []natAddressWire `json:"publicIpv4Addresses"`
	Status              string           `json:"status"`
}

var natAddressType = types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "address": types.StringType}}

func (m *natGatewayModel) fromWire(ctx context.Context, v natGatewayWire) diag.Diagnostics {
	m.ID = types.StringValue(v.Slug)
	m.Name = types.StringValue(v.Name)
	m.VPCID = types.StringValue(v.VPC)
	m.SubnetID = types.StringValue(v.Subnet)
	m.ResourceName = types.StringValue(v.ResourceName)
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
		Description: "Manage an EZGH Cloud NAT gateway: outbound IPv4 internet access for a VPC's private subnets behind one public IPv4 address. Creating a gateway changes no routes; a subnet uses it once its route table has a route to it. A gateway that is replaced (a change of project, VPC or subnet, or a tainted resource) gets a new public IPv4 address: update anything that allowlists the old one.",
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
			"resource_name": schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keep},
			"status":        schema.StringAttribute{Computed: true, Description: "Current service status."},
			"timeouts": schema.SingleNestedAttribute{Optional: true, Description: "How long to wait for the gateway to be active after a create, and gone after a delete. If a create times out the gateway keeps coming up but the resource is tainted: `terraform untaint` keeps it and its address.",
				Attributes: map[string]schema.Attribute{
					"create": schema.StringAttribute{Optional: true, Description: "A duration such as 30m. Default 20m."},
					"delete": schema.StringAttribute{Optional: true, Description: "A duration such as 30m. Default 20m."},
				}},
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
	limit, diags := m.timeout(ctx, "create")
	if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
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
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.wait(ctx, &m, false, limit)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// natTimeout is how long a create or delete is waited on unless the timeouts block says otherwise.
const natTimeout = 20 * time.Minute

var natTimeoutTypes = map[string]attr.Type{"create": types.StringType, "delete": types.StringType}

// timeout is the timeouts block's limit for "create" or "delete", natTimeout without one.
func (m natGatewayModel) timeout(ctx context.Context, operation string) (time.Duration, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.Timeouts.IsNull() || m.Timeouts.IsUnknown() {
		return natTimeout, diags
	}
	value, _ := m.Timeouts.Attributes()[operation].(types.String)
	if value.IsNull() || value.IsUnknown() {
		return natTimeout, diags
	}
	d, err := time.ParseDuration(value.ValueString())
	if err != nil || d <= 0 {
		diags.AddAttributeError(path.Root("timeouts").AtName(operation), "Invalid timeout", fmt.Sprintf("%q isn't a positive duration such as 20m or 1h.", value.ValueString()))
	}
	return d, diags
}

// wait follows an accepted create or delete. The gateway is its own operation: it's read until
// it's active (create), or until reading it answers 404 (delete), for up to limit. A read that
// fails is tried again, whatever the failure: the gateway carries on regardless, and failing a
// create here would taint a gateway that is coming up (its replacement has another address). Only
// the limit, or the gateway itself being gone or deleted under a create, ends a wait in an error.
// A create leaves m as last read.
func (r *natGatewayResource) wait(ctx context.Context, m *natGatewayModel, deleting bool, limit time.Duration) diag.Diagnostics {
	var diags diag.Diagnostics
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	id := m.ID.ValueString()
	var unread error
	for {
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			detail := fmt.Sprintf("NAT gateway %s is still %s after %s; it carries on.", id, m.Status.ValueString(), limit)
			if unread != nil {
				detail += " The last read failed: " + unread.Error()
			}
			if !deleting {
				detail += " Terraform marks it tainted: once it is active, `terraform untaint` keeps it and its public address; otherwise the next apply replaces it, with a new address."
			}
			diags.AddError("Timed out waiting for the NAT gateway", detail)
			return diags
		case <-timer.C:
		}
		response, err := r.client.Do(ctx, "network", "GET", r.path(*m, id), nil)
		if err != nil {
			gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err)
			switch {
			case gone && deleting:
				return diags
			case gone:
				diags.AddError("Create NAT gateway", fmt.Sprintf("NAT gateway %s no longer exists", id))
				return diags
			}
			unread = e
			continue
		}
		read := *m
		if d := read.decode(ctx, response); d.HasError() {
			unread = fmt.Errorf("%s", d[0].Detail())
			continue
		}
		*m, unread = read, nil
		switch status := m.Status.ValueString(); {
		case deleting && status == "deleted", !deleting && status == "active":
			return diags
		case !deleting && (status == "deleting" || status == "deleted"):
			diags.AddError("Create NAT gateway", fmt.Sprintf("NAT gateway %s is %s", id, status))
			return diags
		}
	}
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
	limit, diags := m.timeout(ctx, "delete")
	if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.Do(ctx, "network", "DELETE", r.path(m, m.ID.ValueString()), nil); err != nil {
		if gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err); !gone {
			resp.Diagnostics.AddError("Delete NAT gateway", e.Error())
		}
		return
	}
	// The delete is done, and its public address released, only once the gateway is gone.
	resp.Diagnostics.Append(r.wait(ctx, &m, true, limit)...)
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
