package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/validate"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var verificationTypes = map[string]attr.Type{"txt_name": types.StringType, "txt_value": types.StringType}

type zoneModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Domain        types.String `tfsdk:"domain"`
	DnssecEnabled types.Bool   `tfsdk:"dnssec_enabled"`
	Tags          types.Map    `tfsdk:"tags"`
	Status        types.String `tfsdk:"status"`
	Nameservers   types.List   `tfsdk:"nameservers"`
	Verification  types.Object `tfsdk:"verification"`
}

type zoneView struct {
	Slug, Domain, Status string
	Tags                 map[string]string
	Nameservers          []string
	Verification         struct{ TxtName, TxtValue string }
	Dnssec               struct{ Enabled bool }
}

func (m *zoneModel) fromAPI(ctx context.Context, v zoneView) {
	m.ID, m.Domain, m.Status, m.DnssecEnabled = types.StringValue(v.Slug), types.StringValue(v.Domain), types.StringValue(v.Status), types.BoolValue(v.Dnssec.Enabled)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.Nameservers, _ = types.ListValueFrom(ctx, types.StringType, append([]string{}, v.Nameservers...))
	m.Verification, _ = types.ObjectValue(verificationTypes, map[string]attr.Value{"txt_name": types.StringValue(v.Verification.TxtName), "txt_value": types.StringValue(v.Verification.TxtValue)})
}

type zoneResource struct{ client *client.Client }

func (*zoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}
func (*zoneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "A public DNS zone. Destroying it deletes its record sets; remove record sets that other EZGH services manage (such as a domain authorization's validation records) first.", Attributes: map[string]schema.Attribute{
		"id":             schema.StringAttribute{Computed: true, Description: "Zone ID (zon_).", PlanModifiers: keep},
		"project_id":     schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the zone.", PlanModifiers: replace},
		"domain":         schema.StringAttribute{Required: true, Description: "Domain name of the zone in lowercase, such as example.com, without a trailing dot. Changing it replaces the zone.", PlanModifiers: replace, Validators: []validator.String{validate.Name{}}},
		"dnssec_enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Sign the zone with DNSSEC. Defaults to false."},
		"tags": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
			Description: "Zone tags. The API can't change a zone's tags, and replacing a zone would delete its record sets, so a change is refused at plan."},
		"status":      schema.StringAttribute{Computed: true, Description: "pending until the zone's ownership is verified, then active."},
		"nameservers": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Nameservers to delegate the domain to."},
		"verification": schema.SingleNestedAttribute{Computed: true, Description: "The TXT record that proves control of the domain while the zone is pending.", Attributes: map[string]schema.Attribute{
			"txt_name":  schema.StringAttribute{Computed: true, Description: "TXT record name."},
			"txt_value": schema.StringAttribute{Computed: true, Description: "TXT record value."},
		}},
	}}
}
func (r *zoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *zoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m zoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	body := map[string]any{"domain": m.Domain.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	var v zoneView
	if resp.Diagnostics.HasError() || !r.send(ctx, "POST", zonesPath(r.client, m.ProjectID.ValueString()), body, &v, &resp.Diagnostics) {
		return
	}
	wantDnssec := m.DnssecEnabled.ValueBool()
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...) // saved first: a failed DNSSEC call leaves a tainted zone, not an orphan
	if resp.Diagnostics.HasError() {
		return
	}
	if wantDnssec && !v.Dnssec.Enabled {
		r.setDnssec(ctx, &m, true, &resp.State, &resp.Diagnostics)
	}
}

func (r *zoneResource) setDnssec(ctx context.Context, m *zoneModel, on bool, state *tfsdk.State, diags *diag.Diagnostics) {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	var v zoneView
	if !r.send(ctx, "POST", zonesPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString(), "dnssec", verb), map[string]any{}, &v, diags) {
		return
	}
	m.fromAPI(ctx, v)
	diags.Append(state.Set(ctx, m)...)
}

func (r *zoneResource) send(ctx context.Context, method, path string, body any, v *zoneView, diags *diag.Diagnostics) bool {
	out, err := r.client.Do(ctx, service, method, path, body)
	if err == nil {
		err = out.Decode(v)
	}
	if err == nil && !zoneID.MatchString(v.Slug) {
		err = fmt.Errorf("response omitted the zone identity")
	}
	if err != nil {
		apiError(diags, err)
		return false
	}
	return true
}

func (r *zoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m zoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	var v zoneView
	out, err := r.client.Do(ctx, service, "GET", zonesPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err == nil {
		err = out.Decode(&v)
	}
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); missing {
			resp.State.RemoveResource(ctx)
		} else {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	if v.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update changes DNSSEC, the only attribute a zone can change in place.
func (r *zoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m zoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	r.setDnssec(ctx, &m, m.DnssecEnabled.ValueBool(), &resp.State, &resp.Diagnostics)
}

func (r *zoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m zoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	_, err := r.client.Do(ctx, service, "DELETE", zonesPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes project_id/zon_<12 characters>.
func (r *zoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || validateZoneID(id) != nil {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected project_id/zon_<12-character ID>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ModifyPlan refuses a tag change: the API can't change tags in place, and a replacement would
// delete the zone's record sets.
func (r *zoneResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state types.Map
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("tags"), &plan)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("tags"), &state)...)
	if known(plan) && !plan.Equal(state) {
		resp.Diagnostics.AddAttributeError(path.Root("tags"), "tags can't change", "EZGH DNS can't change a zone's tags, and replacing the zone would delete its record sets. Keep the tags as they are, or create a new zone and move the records.")
	}
}
