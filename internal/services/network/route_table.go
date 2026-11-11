package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type routeTableResource struct{ client *client.Client }
type routeTableModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Tags           types.Map    `tfsdk:"tags"`
	ResourceName   types.String `tfsdk:"resource_name"`
	VPCID          types.String `tfsdk:"vpc_id"`
	Main           types.Bool   `tfsdk:"main"`
	Routes         types.Set    `tfsdk:"routes"`
	ETag           types.String `tfsdk:"etag"`
}
type routeModel struct {
	Destination types.String `tfsdk:"destination"`
	TargetType  types.String `tfsdk:"target_type"`
	TargetID    types.String `tfsdk:"target_id"`
}

// routeWire is a route as written and as read. State (active or blackhole) is read-only and never
// kept: a blackhole route is still the configured route, so it is no drift.
type routeWire struct {
	Destination string `json:"destination"`
	Target      struct {
		Type string  `json:"type"`
		ID   *string `json:"id"`
	} `json:"target"`
}
type routeTableWire struct {
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Description  *string           `json:"description"`
	Tags         map[string]string `json:"tags"`
	ResourceName string            `json:"resourceName"`
	VPC          string            `json:"vpc"`
	Main         bool              `json:"main"`
	Routes       []routeWire       `json:"routes"`
}

func (v *routeTableWire) UnmarshalJSON(b []byte) error {
	type wire routeTableWire
	var decoded wire
	if err := json.Unmarshal(b, &decoded); err != nil {
		return errors.New("invalid route table JSON response")
	}
	if !validSlug(decoded.Slug, "rtb_") || decoded.Name == "" || decoded.ResourceName == "" || !validSlug(decoded.VPC, "vpc_") || decoded.Routes == nil {
		return errors.New("route table response is missing valid identity or required fields")
	}
	*v = routeTableWire(decoded)
	return nil
}

var routeType = types.ObjectType{AttrTypes: map[string]attr.Type{"destination": types.StringType, "target_type": types.StringType, "target_id": types.StringType}}

func (m *routeTableModel) fromWire(ctx context.Context, v routeTableWire, etag string) diag.Diagnostics {
	m.ID = types.StringValue(v.Slug)
	m.Name = types.StringValue(v.Name)
	m.Description = types.StringPointerValue(v.Description)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.ResourceName = types.StringValue(v.ResourceName)
	m.VPCID = types.StringValue(v.VPC)
	m.Main = types.BoolValue(v.Main)
	m.ETag = types.StringValue(etag)
	routes := []routeModel{}
	for _, r := range v.Routes {
		// The VPC's local routes are the service's: never written, never in the set.
		if r.Target.Type != "local" {
			routes = append(routes, routeModel{types.StringValue(r.Destination), types.StringValue(r.Target.Type), types.StringPointerValue(r.Target.ID)})
		}
	}
	var diags diag.Diagnostics
	m.Routes, diags = types.SetValueFrom(ctx, routeType, routes)
	return diags
}

// routesBody is the planned route set as the API takes it.
func (m routeTableModel) routesBody(ctx context.Context) ([]routeWire, diag.Diagnostics) {
	var routes []routeModel
	diags := m.Routes.ElementsAs(ctx, &routes, false)
	wire := make([]routeWire, len(routes))
	for i, r := range routes {
		wire[i].Destination, wire[i].Target.Type, wire[i].Target.ID = r.Destination.ValueString(), r.TargetType.ValueString(), r.TargetID.ValueStringPointer()
	}
	return wire, diags
}

func (r *routeTableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_route_table"
}
func (r *routeTableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := baseAttributes()
	delete(a, "status") // a route table has none
	a["id"] = schema.StringAttribute{Computed: true, Description: "Canonical prefixed resource ID (rtb_).", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	a["resource_name"] = schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	a["vpc_id"] = schema.StringAttribute{Required: true, Description: "VPC (vpc_) the table belongs to. Changing it replaces the table.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	a["main"] = schema.BoolAttribute{Computed: true, Description: "Whether this is the VPC's main table, used by every subnet without a table of its own. A created table never is; an imported main table can't be destroyed (it is deleted with its VPC).", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}
	a["etag"] = schema.StringAttribute{Computed: true, Description: "Version of the route set, used for conditional route replacement."}
	a["routes"] = schema.SetNestedAttribute{Optional: true, Computed: true, Default: setdefault.StaticValue(types.SetValueMust(routeType, nil)),
		Description: "Complete set of the table's routes, one per destination. Omitted or [] means no routes. The VPC's local routes are always present and are not listed. A route whose target is deleted, detached or not yet active stays configured and carries no traffic until the target is back.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"destination": schema.StringAttribute{Required: true, Description: "Canonical destination CIDR, IPv4 or IPv6, such as 0.0.0.0/0."},
			"target_type": schema.StringAttribute{Required: true, Description: "networkGateway, networkInterface or natGateway."},
			"target_id":   schema.StringAttribute{Required: true, Description: "The target's ID: the VPC's attached Network Gateway (ngw_), a private network interface in the VPC (eni_) or a NAT gateway of the VPC (nat_)."},
		}}}
	resp.Schema = schema.Schema{Description: "Manage a VPC route table and its complete set of routes. A subnet uses the table once its `route_table_id` names it. Do not manage the same table's routes with another resource or tool.", Attributes: a}
}
func (r *routeTableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", fmt.Sprintf("Expected API client, got %T", req.ProviderData))
	}
}

// ValidateConfig refuses a destination the API would store in another spelling (host bits, case),
// which would otherwise surface as an inconsistent result after apply.
func (r *routeTableResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m routeTableModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Routes.IsNull() || m.Routes.IsUnknown() {
		return
	}
	var routes []routeModel
	resp.Diagnostics.Append(m.Routes.ElementsAs(ctx, &routes, false)...)
	seen := map[string]bool{}
	for _, route := range routes {
		if route.Destination.IsUnknown() {
			continue
		}
		d := route.Destination.ValueString()
		if p, err := netip.ParsePrefix(d); err != nil || p.Masked().String() != d {
			resp.Diagnostics.AddAttributeError(path.Root("routes"), "Invalid route destination", fmt.Sprintf("%q isn't a canonical CIDR such as 0.0.0.0/0 or 10.0.1.0/24.", d))
		}
		if seen[d] {
			resp.Diagnostics.AddAttributeError(path.Root("routes"), "Duplicate route destination", fmt.Sprintf("%q has more than one route.", d))
		}
		seen[d] = true
	}
}

func (r *routeTableResource) path(m routeTableModel, id ...string) string {
	return client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "route-tables", id...)
}

// decode sets m from a response; a response without the table's identity never replaces the state.
func (m *routeTableModel) decode(ctx context.Context, response *client.Response) diag.Diagnostics {
	var v routeTableWire
	if err := response.Decode(&v); err != nil {
		var diags diag.Diagnostics
		diags.AddError("Decode route table", err.Error())
		return diags
	}
	return m.fromWire(ctx, v, response.Header.Get("ETag"))
}

func (r *routeTableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m routeTableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	var tags map[string]string
	resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	routes, diags := m.routesBody(ctx)
	if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
		return
	}
	response, err := r.client.Do(ctx, "network", "POST", r.path(m), map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags, "vpc": m.VPCID.ValueString(), "routes": routes})
	if err != nil {
		resp.Diagnostics.AddError("Create route table", err.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routeTableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m routeTableModel
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
		resp.Diagnostics.AddError("Read route table", e.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routeTableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior routeTableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	p := r.path(m, m.ID.ValueString())
	if !m.Name.Equal(prior.Name) || !m.Description.Equal(prior.Description) || !m.Tags.Equal(prior.Tags) {
		var tags map[string]string
		resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if _, err := r.client.Do(ctx, "network", "PATCH", p, map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags}); err != nil {
			resp.Diagnostics.AddError("Update route table", err.Error())
			return
		}
	}
	if !m.Routes.Equal(prior.Routes) {
		// The ETag is the route set's version (a rename doesn't move it): the replacement applies
		// only to the routes this plan was made from, and the service answers 412 otherwise.
		if prior.ETag.ValueString() == "" {
			resp.Diagnostics.AddError("Missing route table version", "Service did not return an ETag; refusing unconditional route replacement.")
			return
		}
		routes, diags := m.routesBody(ctx)
		if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
			return
		}
		if _, err := r.client.DoWithHeaders(ctx, "network", "PUT", p+"/routes", map[string]any{"routes": routes}, http.Header{"If-Match": []string{prior.ETag.ValueString()}}); err != nil {
			resp.Diagnostics.AddError("Replace routes", err.Error()+"\n\nIf the routes changed since the last refresh, create a new plan.")
			return
		}
	}
	response, err := r.client.Do(ctx, "network", "GET", p, nil)
	if err != nil {
		resp.Diagnostics.AddError("Refresh route table", err.Error())
		return
	}
	resp.Diagnostics.Append(m.decode(ctx, response)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routeTableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m routeTableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	if err := deleteResource(ctx, r.client, "network", m.ProjectID.ValueString(), "route-tables", m.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete route table", err.Error())
	}
}
func (r *routeTableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, project, id, err := parseImport(req.ID, "rtb_")
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
