package dns

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// zoneDataSource reads a zone by id or by domain, in a project.
type zoneDataSource struct{ client *client.Client }

func (*zoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}
func (*zoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read an existing EZGH DNS zone by its ID or its domain.", Attributes: map[string]schema.Attribute{
		"project_id":     schema.StringAttribute{Required: true, Description: "Project identifier."},
		"id":             schema.StringAttribute{Optional: true, Computed: true, Description: "Zone ID (zon_). Set id or domain."},
		"domain":         schema.StringAttribute{Optional: true, Computed: true, Description: "Domain name of the zone. Set id or domain."},
		"dnssec_enabled": schema.BoolAttribute{Computed: true, Description: "Whether the zone is signed with DNSSEC."},
		"tags":           schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Zone tags."},
		"status":         schema.StringAttribute{Computed: true, Description: "pending until the zone's ownership is verified, then active."},
		"nameservers":    schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Nameservers to delegate the domain to."},
		"verification": schema.SingleNestedAttribute{Computed: true, Description: "The TXT record that proves control of the domain while the zone is pending.", Attributes: map[string]schema.Attribute{
			"txt_name":  schema.StringAttribute{Computed: true, Description: "TXT record name."},
			"txt_value": schema.StringAttribute{Computed: true, Description: "TXT record value."},
		}},
	}}
}
func (d *zoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *zoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if missingProvider(&resp.Diagnostics, d.client) {
		return
	}
	var m zoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, id, domain := m.ProjectID.ValueString(), m.ID.ValueString(), m.Domain.ValueString()
	if (id == "") == (domain == "") {
		resp.Diagnostics.AddError("Set id or domain", "Look a zone up by exactly one of id or domain.")
		return
	}
	var raw []byte
	if id != "" {
		if err := validateZoneID(id); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid zone ID", err.Error())
			return
		}
		out, err := d.client.Do(ctx, service, "GET", zonesPath(d.client, project, id), nil)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		raw = out.Body
	} else {
		zones, err := d.client.List(ctx, service, zonesPath(d.client, project)+"?domainPrefix="+url.QueryEscape(domain))
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		for _, z := range zones {
			if z["domain"] == domain {
				raw, _ = json.Marshal(z)
				break
			}
		}
		if raw == nil {
			resp.Diagnostics.AddError("Zone not found", "No zone for "+domain+" in the project.")
			return
		}
	}
	var v zoneView
	if err := json.Unmarshal(raw, &v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
