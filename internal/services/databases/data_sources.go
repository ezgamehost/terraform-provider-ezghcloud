package databases

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

// lookup reads a database or backup by id and project_id, with the resource's API attributes.
type lookup struct {
	kind   string
	client *client.Client
}

// providerOnly are resource attributes the API doesn't show.
var providerOnly = map[string]bool{"source_backup_id": true, "source_database_id": true, "source_time": true, "operation_id": true, "operation_timeout": true, "final_backup_on_destroy": true}

func (d *lookup) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind
}
func (d *lookup) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	var r resource.Resource = &databaseResource{}
	if d.kind == "database_backup" {
		r = &backupResource{}
	}
	var rs resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &rs)
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
		case types.SetType:
			attrs[name] = schema.SetAttribute{Computed: true, ElementType: t.ElemType, Description: desc}
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
	attrs["id"] = schema.StringAttribute{Required: true, Description: "Canonical prefixed ID."}
	attrs["project_id"] = schema.StringAttribute{Required: true, Description: "Project identifier."}
	resp.Schema = schema.Schema{Description: "Read an existing EZGH Cloud " + d.kind + " by its ID.", Attributes: attrs}
}
func (d *lookup) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *lookup) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	prefix := map[string]string{"database": "dbi_", "database_backup": "dbb_"}[d.kind]
	if !strings.HasPrefix(id.ValueString(), prefix) || !prefixedID.MatchString(id.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", "Use the canonical "+prefix+" ID.")
		return
	}
	if d.kind == "database_backup" {
		var f backupFields
		resp.Diagnostics.Append(req.Config.Get(ctx, &f)...)
		if resp.Diagnostics.HasError() {
			return
		}
		v, err := readBackup(ctx, d.client, f.ProjectID.ValueString(), f.ID.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		f.fill(v, d.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &f)...)
		return
	}
	var f databaseFields
	resp.Diagnostics.Append(req.Config.Get(ctx, &f)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := readDatabase(ctx, d.client, f.ProjectID.ValueString(), f.ID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	f.fill(ctx, v, d.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &f)...)
}
