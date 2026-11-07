package waf

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// managedRuleSetsDataSource lists the managed rule sets a WAF can run.
type managedRuleSetsDataSource struct{ client *client.Client }

type managedRuleSetModel struct {
	Name        string `tfsdk:"name"`
	Description string `tfsdk:"description"`
	Version     string `tfsdk:"version"`
	UpdatedAt   string `tfsdk:"updated_at"`
}

func (*managedRuleSetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_waf_managed_rule_sets"
}
func (*managedRuleSetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "The managed rule sets a WAF can run: Core rule set (OWASP CRS), PHP, Node.js and Known bad inputs.", Attributes: map[string]schema.Attribute{
		"managed_rule_sets": schema.ListNestedAttribute{Computed: true, Description: "Every managed rule set.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Computed: true, Description: "Name to use in a WAF's managed_rule_sets, such as ezgh/core."},
			"description": schema.StringAttribute{Computed: true, Description: "What the set protects against."},
			"version":     schema.StringAttribute{Computed: true, Description: "Current version."},
			"updated_at":  schema.StringAttribute{Computed: true, Description: "When the current version was published."},
		}}},
	}}
}
func (d *managedRuleSetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *managedRuleSetsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	rows, err := d.client.List(ctx, service, client.OrgPath(d.client.OrganizationID, "managedRuleSets"))
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	sets := []managedRuleSetModel{}
	for _, row := range rows {
		s := func(k string) string { v, _ := row[k].(string); return v }
		sets = append(sets, managedRuleSetModel{Name: s("name"), Description: s("description"), Version: s("version"), UpdatedAt: s("updatedAt")})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &struct {
		Sets []managedRuleSetModel `tfsdk:"managed_rule_sets"`
	}{sets})...)
}
