package warehouse

import (
	"context"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// lookup reads a warehouse by id and project_id, with the resource's API attributes.
type lookup struct{ client *client.Client }

// providerOnly are resource attributes the API doesn't show.
var providerOnly = map[string]bool{"operation_id": true, "operation_timeout": true, "from_backup": true, "app_secret_id": true}

func (d *lookup) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_warehouse"
}
func (d *lookup) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	var rs resource.SchemaResponse
	(&warehouseResource{}).Schema(ctx, resource.SchemaRequest{}, &rs)
	attrs := map[string]schema.Attribute{}
	for name, a := range rs.Schema.Attributes {
		if providerOnly[name] {
			continue
		}
		desc := a.GetDescription()
		switch t := a.GetType().(type) {
		case types.MapType:
			attrs[name] = schema.MapAttribute{Computed: true, ElementType: t.ElemType, Description: desc}
		case types.ListType:
			attrs[name] = schema.ListAttribute{Computed: true, ElementType: t.ElemType, Description: desc}
		case types.ObjectType:
			attrs[name] = schema.ObjectAttribute{Computed: true, AttributeTypes: t.AttrTypes, Description: desc}
		default:
			switch a.GetType() {
			case types.Int64Type:
				attrs[name] = schema.Int64Attribute{Computed: true, Description: desc}
			case types.BoolType:
				attrs[name] = schema.BoolAttribute{Computed: true, Description: desc}
			default:
				attrs[name] = schema.StringAttribute{Computed: true, Description: desc}
			}
		}
	}
	attrs["id"] = schema.StringAttribute{Required: true, Description: "Warehouse ID (whs_)."}
	attrs["project_id"] = schema.StringAttribute{Required: true, Description: "Project identifier."}
	resp.Schema = schema.Schema{Description: "Read an existing EZGH Warehouse warehouse by its ID.", Attributes: attrs}
}
func (d *lookup) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *lookup) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	var f warehouseFields
	resp.Diagnostics.Append(req.Config.Get(ctx, &f)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !strings.HasPrefix(f.ID.ValueString(), "whs_") || !prefixedID.MatchString(f.ID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", "Use the canonical whs_ ID.")
		return
	}
	v, err := readWarehouse(ctx, d.client, f.ProjectID.ValueString(), f.ID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	f.fill(ctx, v, d.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &f)...)
}
