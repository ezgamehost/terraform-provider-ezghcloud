package certificates

import (
	"context"
	"fmt"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	instructionType = types.ObjectType{AttrTypes: map[string]attr.Type{"type": types.StringType, "name": types.StringType, "value": types.StringType}}
	managedDNSTypes = map[string]attr.Type{"zone_id": types.StringType, "status": types.StringType, "detail": types.StringType}
)

type authorizationModel struct {
	ID              types.String `tfsdk:"id"`
	DomainName      types.String `tfsdk:"domain_name"`
	Name            types.String `tfsdk:"name"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	Tags            types.Map    `tfsdk:"tags"`
	DNSZoneID       types.String `tfsdk:"dns_zone_id"`
	Status          types.String `tfsdk:"status"`
	DNSInstructions types.List   `tfsdk:"dns_instructions"`
	ManagedDNS      types.Object `tfsdk:"managed_dns"`
}

type authorizationView struct {
	ID, Slug, Name, DomainName, Status string
	Enabled                            bool
	Tags                               map[string]string
	DNSInstructions                    []struct{ Type, Name, Value string }
	DNSZoneID                          *string
	ManagedDNS                         *struct {
		ZoneID, Status string
		Detail         *string
	}
}

func (m *authorizationModel) fromAPI(ctx context.Context, v authorizationView) {
	m.ID, m.DomainName, m.Name, m.Enabled, m.Status = types.StringValue(v.Slug), types.StringValue(v.DomainName), types.StringValue(v.Name), types.BoolValue(v.Enabled), types.StringValue(v.Status)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.DNSZoneID = types.StringPointerValue(v.DNSZoneID)
	items := []attr.Value{}
	for _, i := range v.DNSInstructions {
		items = append(items, types.ObjectValueMust(instructionType.AttrTypes, map[string]attr.Value{"type": types.StringValue(i.Type), "name": types.StringValue(i.Name), "value": types.StringValue(i.Value)}))
	}
	m.DNSInstructions = types.ListValueMust(instructionType, items)
	m.ManagedDNS = types.ObjectNull(managedDNSTypes)
	if v.ManagedDNS != nil {
		m.ManagedDNS = types.ObjectValueMust(managedDNSTypes, map[string]attr.Value{"zone_id": types.StringValue(v.ManagedDNS.ZoneID), "status": types.StringValue(v.ManagedDNS.Status), "detail": types.StringPointerValue(v.ManagedDNS.Detail)})
	}
}

// warnManagedDNS tells the customer when the zone didn't get the validation records; the apply
// itself succeeded, and linking the zone again heals it.
func warnManagedDNS(d *diag.Diagnostics, v authorizationView) {
	if v.ManagedDNS == nil || v.ManagedDNS.Status != "failed" {
		return
	}
	detail := "The validation records could not be written to zone " + v.ManagedDNS.ZoneID + "."
	if v.ManagedDNS.Detail != nil {
		detail += " " + *v.ManagedDNS.Detail
	}
	d.AddWarning("Domain authorization is not linked to its DNS zone", detail)
}

type authorizationResource struct{ client *client.Client }

func (*authorizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_domain_authorization"
}
func (*authorizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{Description: "Permission for the organization to get Web Certificates for a domain and its subdomains. Set dns_zone_id to have EZGH write the validation records into one of your EZGH DNS zones; otherwise add the records in dns_instructions yourself.", Attributes: map[string]schema.Attribute{
		"id":          schema.StringAttribute{Computed: true, Description: "Domain authorization ID (dau_).", PlanModifiers: keep},
		"domain_name": schema.StringAttribute{Required: true, Description: "Domain name to authorize. Changing it replaces the authorization.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":        schema.StringAttribute{Optional: true, Computed: true, Description: "Name, 1 to 100 characters. Defaults to the domain name.", PlanModifiers: keep},
		"enabled":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Whether certificates can be issued for the domain. Defaults to true."},
		"tags":        schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"dns_zone_id": schema.StringAttribute{Optional: true, Description: "EZGH DNS zone (zon_) that receives the validation records. Removing it unlinks the zone and removes the records EZGH wrote."},
		"status":      schema.StringAttribute{Computed: true, Description: "pending until the validation records are seen, then verified."},
		"dns_instructions": schema.ListNestedAttribute{Computed: true, Description: "The records that prove control of the domain.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"type":  schema.StringAttribute{Computed: true, Description: "Record type."},
			"name":  schema.StringAttribute{Computed: true, Description: "Record name."},
			"value": schema.StringAttribute{Computed: true, Description: "Record value."},
		}}},
		"managed_dns": schema.SingleNestedAttribute{Computed: true, Description: "State of the records EZGH writes into the linked zone, or null when no zone is linked.", Attributes: map[string]schema.Attribute{
			"zone_id": schema.StringAttribute{Computed: true, Description: "The linked zone."},
			"status":  schema.StringAttribute{Computed: true, Description: "synced, pending or failed."},
			"detail":  schema.StringAttribute{Computed: true, Description: "Why the records failed, when they did."},
		}},
	}}
}
func (r *authorizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *authorizationResource) path(id ...string) string {
	return client.OrgPath(r.client.OrganizationID, "domain-authorizations", id...)
}

// call sends one request and decodes the authorization it answers with.
func (r *authorizationResource) call(ctx context.Context, method, path string, body any, wantStatus int, diags *diag.Diagnostics) (authorizationView, bool) {
	var v authorizationView
	out, err := r.client.Do(ctx, service, method, path, body)
	if err == nil {
		err = out.Decode(&v)
	}
	if err == nil && out.StatusCode != wantStatus {
		err = fmt.Errorf("the domain authorization already exists; import it with its ID (dau_) to manage it")
	}
	if err == nil && !strings.HasPrefix(v.Slug, "dau_") {
		err = fmt.Errorf("response omitted the domain authorization identity")
	}
	if err != nil {
		apiError(diags, err)
		return v, false
	}
	return v, true
}

func (r *authorizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m authorizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	body := map[string]any{"domainName": m.DomainName.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if known(m.Name) {
		body["name"] = m.Name.ValueString()
	}
	if known(m.DNSZoneID) {
		body["dnsZoneId"] = m.DNSZoneID.ValueString()
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, ok := r.call(ctx, "POST", r.path(), body, 201, &resp.Diagnostics)
	if !ok {
		return
	}
	if !m.Enabled.ValueBool() {
		if v, ok = r.call(ctx, "PATCH", r.path(v.Slug), map[string]any{"enabled": false}, 200, &resp.Diagnostics); !ok {
			return
		}
	}
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	warnManagedDNS(&resp.Diagnostics, v)
}

func (r *authorizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m authorizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	out, err := r.client.Do(ctx, service, "GET", r.path(m.ID.ValueString()), nil)
	var v authorizationView
	if err == nil {
		err = out.Decode(&v)
	}
	if isStatus(err, 404) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update sends the changed fields. A removed dns_zone_id is sent as null, which unlinks the zone.
func (r *authorizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old authorizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "enabled": m.Enabled.ValueBool(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if !m.DNSZoneID.Equal(old.DNSZoneID) {
		body["dnsZoneId"] = m.DNSZoneID.ValueStringPointer()
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, ok := r.call(ctx, "PATCH", r.path(old.ID.ValueString()), body, 200, &resp.Diagnostics)
	if !ok {
		return
	}
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	warnManagedDNS(&resp.Diagnostics, v)
}

func (r *authorizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	if _, err := r.client.Do(ctx, service, "DELETE", r.path(id.ValueString()), nil); err != nil && !isStatus(err, 404) {
		apiError(&resp.Diagnostics, err)
	}
}

func (r *authorizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, "dau_") || !prefixedID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected dau_<12-character ID>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
