package kubernetes

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

// clusterDataSource reads a cluster by id and project_id: its endpoint and CA for the kubernetes
// provider, and every attribute the resource shows. No token: it would put a bearer in state;
// configure the kubernetes provider's exec with `ezgh kubernetes token` instead.
type clusterDataSource struct{ client *client.Client }

func (*clusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}
func (*clusterDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	var rs resource.SchemaResponse
	(&clusterResource{}).Schema(ctx, resource.SchemaRequest{}, &rs)
	attrs := map[string]schema.Attribute{}
	for name, a := range rs.Schema.Attributes {
		if name == "bootstrap_creator_access" || name == "operation_id" || name == "operation_timeout" {
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
	attrs["id"] = schema.StringAttribute{Required: true, Description: "Cluster ID (kcl_)."}
	attrs["project_id"] = schema.StringAttribute{Required: true, Description: "Project identifier."}
	resp.Schema = schema.Schema{Description: "Read an EZGH Kubernetes cluster by its ID: endpoint_hostname and certificate_authority configure the kubernetes provider.", Attributes: attrs}
}
func (d *clusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *clusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	var id, project types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &project)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !strings.HasPrefix(id.ValueString(), "kcl_") || !prefixedID.MatchString(id.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", "Use the cluster's kcl_ ID.")
		return
	}
	var v clusterView
	if err := get(ctx, d.client, clusterPath(d.client, project.ValueString(), id.ValueString()), "kcl_", &v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	f := clusterFields{ProjectID: project}
	f.fill(ctx, v, d.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &f)...)
}
