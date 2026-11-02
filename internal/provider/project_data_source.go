package provider

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type projectDataSource struct{ c *client.Client }

func (r *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}
func (r *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Look up an existing project by API identifier.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Required: true, Description: "Project API identifier."},
		"slug":            schema.StringAttribute{Computed: true, Description: "Canonical project identifier used in resource names."},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization from the provider configuration."},
		"name":            schema.StringAttribute{Computed: true, Description: "Project name."},
		"folder_id":       schema.StringAttribute{Computed: true, Description: "Folder identifier, or null for the organization root."},
	}}
}
func (r *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.c, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "Expected an EZGH Cloud API client.")
	}
}
func (r *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.Organization = types.StringValue(r.c.OrganizationID)
	out, err := r.c.Do(ctx, "orgs", "GET", client.OrgPath(r.c.OrganizationID, "projects", m.ID.ValueString()), nil)
	if err == nil {
		err = m.decode(out)
	}
	if err != nil {
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
