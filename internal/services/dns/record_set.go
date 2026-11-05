package dns

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var managedByTypes = map[string]attr.Type{"service": types.StringType, "resource_name": types.StringType}

type recordSetModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	ZoneID    types.String `tfsdk:"zone_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	TTL       types.Int64  `tfsdk:"ttl"`
	Values    types.Set    `tfsdk:"values"`
	ManagedBy types.Object `tfsdk:"managed_by"`
}

type recordSetView struct {
	Name, Type string
	TTL        int64
	Values     []string
	ManagedBy  *struct{ Service, ResourceName string }
}

type recordSetResource struct{ client *client.Client }

func (*recordSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record_set"
}
func (*recordSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "One record set (a name and type) of a zone. Record sets that another EZGH service manages, such as a domain authorization's validation records, can't be created, changed or deleted here.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, Description: "zone_id/name/TYPE.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id": schema.StringAttribute{Required: true, Description: "Project that holds the zone. Changing it replaces the record set.", PlanModifiers: replace},
		"zone_id":    schema.StringAttribute{Required: true, Description: "Zone ID (zon_). Changing it replaces the record set.", PlanModifiers: replace},
		"name":       schema.StringAttribute{Required: true, Description: "Lowercase name relative to the zone, or @ for the zone itself. Changing it replaces the record set.", PlanModifiers: replace},
		"type":       schema.StringAttribute{Required: true, Description: "Record type, such as A, AAAA, CNAME or TXT. Changing it replaces the record set.", PlanModifiers: replace},
		"ttl":        schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(300), Description: "Time to live in seconds. Defaults to 300."},
		"values":     schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "The record set's values."},
		"managed_by": schema.SingleNestedAttribute{Computed: true, Description: "The EZGH service that manages this record set, or null for a record set you manage.", PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}, Attributes: map[string]schema.Attribute{
			"service":       schema.StringAttribute{Computed: true, Description: "Managing service namespace."},
			"resource_name": schema.StringAttribute{Computed: true, Description: "Resource name of the managing resource."},
		}},
	}}
}
func (r *recordSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (m *recordSetModel) fromAPI(ctx context.Context, v recordSetView) {
	m.ID = types.StringValue(m.ZoneID.ValueString() + "/" + m.Name.ValueString() + "/" + m.Type.ValueString())
	m.TTL = types.Int64Value(v.TTL)
	m.Values, _ = types.SetValueFrom(ctx, types.StringType, append([]string{}, v.Values...))
	m.ManagedBy = types.ObjectNull(managedByTypes)
	if v.ManagedBy != nil {
		m.ManagedBy, _ = types.ObjectValue(managedByTypes, map[string]attr.Value{"service": types.StringValue(v.ManagedBy.Service), "resource_name": types.StringValue(v.ManagedBy.ResourceName)})
	}
}

// find pages through a zone's record sets for name and type; nil when there is none.
func find(ctx context.Context, c *client.Client, project, zone, name, typ string) (*recordSetView, error) {
	p := zonesPath(c, project, zone, "records")
	for {
		out, err := c.Do(ctx, service, "GET", p, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			RecordSets []recordSetView
			NextCursor *string
		}
		if err = out.Decode(&page); err != nil {
			return nil, err
		}
		for _, s := range page.RecordSets {
			if s.Name == name && s.Type == typ {
				return &s, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return nil, nil
		}
		p = zonesPath(c, project, zone, "records") + "?cursor=" + url.QueryEscape(*page.NextCursor)
	}
}

func (r *recordSetResource) change(ctx context.Context, m *recordSetModel, ch map[string]any) error {
	ch["name"], ch["type"] = m.Name.ValueString(), m.Type.ValueString()
	_, err := r.client.Do(ctx, service, "POST", zonesPath(r.client, m.ProjectID.ValueString(), m.ZoneID.ValueString(), "records", "changes"), map[string]any{"changes": []any{ch}})
	return err
}

// apply upserts the planned record set and reads it back into m. A create refuses to take over a
// record set that is already there.
func (r *recordSetResource) apply(ctx context.Context, m *recordSetModel, creating bool, diags *diag.Diagnostics) bool {
	if missingProvider(diags, r.client) {
		return false
	}
	if err := validateZoneID(m.ZoneID.ValueString()); err != nil {
		diags.AddAttributeError(path.Root("zone_id"), "Invalid zone ID", err.Error())
		return false
	}
	project, zone, name, typ := m.ProjectID.ValueString(), m.ZoneID.ValueString(), m.Name.ValueString(), m.Type.ValueString()
	if creating {
		old, err := find(ctx, r.client, project, zone, name, typ)
		if err != nil {
			apiError(diags, err)
			return false
		}
		if old != nil {
			diags.AddError("Record set already exists", fmt.Sprintf("%s %s is already in %s. Import it as %s/%s/%s, or remove it first.", name, typ, zone, zone, name, typ))
			return false
		}
	}
	var values []string
	diags.Append(m.Values.ElementsAs(ctx, &values, false)...)
	if diags.HasError() {
		return false
	}
	if err := r.change(ctx, m, map[string]any{"action": "upsert", "ttl": m.TTL.ValueInt64(), "values": values}); err != nil {
		apiError(diags, err)
		return false
	}
	return r.refresh(ctx, m, diags)
}

func (r *recordSetResource) refresh(ctx context.Context, m *recordSetModel, diags *diag.Diagnostics) bool {
	v, err := find(ctx, r.client, m.ProjectID.ValueString(), m.ZoneID.ValueString(), m.Name.ValueString(), m.Type.ValueString())
	if err == nil && v == nil {
		err = fmt.Errorf("the record set is not in the zone after the change")
	}
	if err != nil {
		apiError(diags, err)
		return false
	}
	m.fromAPI(ctx, *v)
	return true
}

func (r *recordSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m recordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() && r.apply(ctx, &m, true, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}
func (r *recordSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m recordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() && r.apply(ctx, &m, false, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}
func (r *recordSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m recordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	v, err := find(ctx, r.client, m.ProjectID.ValueString(), m.ZoneID.ValueString(), m.Name.ValueString(), m.Type.ValueString())
	if err == nil && v == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); missing {
			resp.State.RemoveResource(ctx)
		} else {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	m.fromAPI(ctx, *v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *recordSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m recordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	// The API refuses deleting a set that isn't there, so check first.
	v, err := find(ctx, r.client, m.ProjectID.ValueString(), m.ZoneID.ValueString(), m.Name.ValueString(), m.Type.ValueString())
	if err == nil && v != nil {
		err = r.change(ctx, &m, map[string]any{"action": "delete"})
	}
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes zone_id/name/TYPE or project_id/zone_id/name/TYPE. Without the project, it
// finds the zone by trying the organization's projects.
func (r *recordSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	project := ""
	if len(parts) == 4 {
		project, parts = parts[0], parts[1:]
	}
	if len(parts) != 3 || validateZoneID(parts[0]) != nil || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected zone_id/name/TYPE or project_id/zone_id/name/TYPE, such as zon_abcdefghij12/www/A.")
		return
	}
	if project == "" {
		if missingProvider(&resp.Diagnostics, r.client) {
			return
		}
		projects, err := r.client.List(ctx, "orgs", client.OrgPath(r.client.OrganizationID, "projects"))
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		for _, p := range projects {
			slug, _ := p["slug"].(string)
			if _, err := r.client.Do(ctx, service, "GET", zonesPath(r.client, slug, parts[0]), nil); slug != "" && err == nil {
				project = slug
				break
			}
		}
		if project == "" {
			resp.Diagnostics.AddError("Zone not found", parts[0]+" is not in any project you can access.")
			return
		}
	}
	for k, v := range map[string]string{"project_id": project, "zone_id": parts[0], "name": parts[1], "type": parts[2]} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.Join(parts, "/"))...)
}
