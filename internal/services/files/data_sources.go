package files

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

// lookup reads a file system or mount target by id and project_id, with the resource's attributes.
type lookup struct {
	kind   string
	client *client.Client
}

func (d *lookup) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind
}
func (d *lookup) resource() (resource.Resource, string, string) {
	if d.kind == "mount_target" {
		return &mountTargetResource{}, "mount-targets", "fmt_"
	}
	return &fileSystemResource{}, "file-systems", "fsy_"
}
func (d *lookup) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	r, _, _ := d.resource()
	var rs resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &rs)
	attrs := map[string]schema.Attribute{}
	for name, a := range rs.Schema.Attributes {
		desc := a.GetDescription()
		switch t := a.GetType().(type) {
		case types.MapType:
			attrs[name] = schema.MapAttribute{Computed: true, ElementType: t.ElemType, Description: desc}
		case types.ListType:
			attrs[name] = schema.ListAttribute{Computed: true, ElementType: t.ElemType, Description: desc}
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
	resp.Schema = schema.Schema{Description: "Read an existing EZGH Cloud " + strings.ReplaceAll(d.kind, "_", " ") + " by its ID.", Attributes: attrs}
}
func (d *lookup) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *lookup) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	_, collection, prefix := d.resource()
	var id, project types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &project)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !strings.HasPrefix(id.ValueString(), prefix) || !prefixedID.MatchString(id.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", "Use the canonical "+prefix+" ID.")
		return
	}
	var m any
	if d.kind == "mount_target" {
		var v mountTargetView
		err := read(ctx, d.client, project.ValueString(), collection, id.ValueString(), prefix, &v)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		mt := mountTargetModel{common: common{ProjectID: project}}
		mt.fromAPI(ctx, v, d.client.OrganizationID)
		m = &mt
	} else {
		var v fileSystemView
		err := read(ctx, d.client, project.ValueString(), collection, id.ValueString(), prefix, &v)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		fs := fileSystemModel{common: common{ProjectID: project}}
		fs.fromAPI(ctx, v, d.client.OrganizationID)
		m = &fs
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
