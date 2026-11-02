package iam

import (
	"context"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type iamDataSource struct {
	definition definition
	client     *client.Client
}

func DataSources() []func() datasource.DataSource {
	out := []func() datasource.DataSource{}
	for _, name := range []string{"user", "bot", "group", "policy", "oidc_provider"} {
		n := name
		out = append(out, func() datasource.DataSource {
			var d definition
			if n == "user" {
				d = definition{name: "user", collection: "users", fields: map[string]field{"id": str("id", true, false, false), "organization_id": str("", false, true, false), "name": str("name", false, true, false), "email": str("email", false, true, false)}}
			} else {
				d = definitions()[n]
			}
			for k, f := range d.fields {
				f.output = true
				d.fields[k] = f
			}
			d.kind = "lookup"
			return &iamDataSource{definition: d}
		})
	}
	return out
}
func (d *iamDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.definition.name
}
func (d *iamDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]dschema.Attribute{}
	for k, f := range d.definition.fields {
		switch f.typ {
		case types.StringType:
			attrs[k] = dschema.StringAttribute{Required: k == "id", Computed: k != "id", Description: "The " + k + " value."}
		case types.Int64Type:
			attrs[k] = dschema.Int64Attribute{Computed: true}
		default:
			switch t := f.typ.(type) {
			case types.SetType:
				attrs[k] = dschema.SetAttribute{Computed: true, ElementType: t.ElemType}
			case types.ListType:
				attrs[k] = dschema.ListAttribute{Computed: true, ElementType: t.ElemType}
			case types.MapType:
				attrs[k] = dschema.MapAttribute{Computed: true, ElementType: t.ElemType}
			}
		}
	}
	resp.Schema = dschema.Schema{Description: "Look up an existing EZGH Cloud " + d.definition.name + " by API ID.", Attributes: attrs}
	_ = ctx
}
func (d *iamDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "Expected EZGH Cloud API client.")
		return
	}
	d.client = c
}
func (d *iamDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	v, e := readRaw(ctx, d.definition, req.Config.Raw)
	if e != nil {
		resp.Diagnostics.AddError("Invalid lookup", e.Error())
		return
	}
	v["organization_id"] = types.StringValue(d.client.OrganizationID)
	v, missing, e := readEntity(ctx, d.client, d.definition, v)
	if e != nil {
		resp.Diagnostics.AddError("Unable to read IAM identity", e.Error())
		return
	}
	if missing {
		resp.Diagnostics.AddError("IAM identity not found", "No identity exists with that ID in the configured organization.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, objectValue(d.definition, v))...)
}
