package kubernetes

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type associationModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	ClusterID      types.String `tfsdk:"cluster_id"`
	ResourceName   types.String `tfsdk:"resource_name"`
	Namespace      types.String `tfsdk:"namespace"`
	ServiceAccount types.String `tfsdk:"service_account"`
	BotID          types.String `tfsdk:"bot_id"`
	Tags           types.Map    `tfsdk:"tags"`
}
type associationView struct {
	Slug, ResourceName, Cluster, Namespace, ServiceAccount, Bot string
	Tags                                                        map[string]string
}

type associationResource struct{ client *client.Client }

func (*associationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_pod_identity_association"
}
func (*associationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A pod identity association: pods using a service account get short-lived keys for a Bot. Nothing is stored in the cluster. The caller needs bots.pass on the Bot.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "Pod identity association ID (kpa_).", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "The cluster's project. Changing it replaces the association.", PlanModifiers: replace},
		"cluster_id":      schema.StringAttribute{Required: true, Description: "Cluster (kcl_). Changing it replaces the association.", PlanModifiers: replace},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"namespace":       schema.StringAttribute{Required: true, Description: "Kubernetes namespace. Changing it replaces the association.", PlanModifiers: replace},
		"service_account": schema.StringAttribute{Required: true, Description: "Kubernetes service account in the namespace. Changing it replaces the association.", PlanModifiers: replace},
		"bot_id":          schema.StringAttribute{Required: true, Description: "Bot (bot_) the pods get keys for. Changes in place; the next key is for the new Bot."},
		"tags":            schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
	}}
}
func (r *associationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *associationResource) path(m associationModel, id ...string) string {
	return clusterPath(r.client, m.ProjectID.ValueString(), m.ClusterID.ValueString(), append([]string{"pod-identity-associations"}, id...)...)
}

func (r *associationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m associationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"namespace": m.Namespace.ValueString(), "serviceAccount": m.ServiceAccount.ValueString(), "bot": m.BotID.ValueString(), "tags": stringMap(ctx, m.Tags, &resp.Diagnostics)}
	if !resp.Diagnostics.HasError() {
		r.send(ctx, "POST", r.path(m), body, &m, &resp.State, &resp.Diagnostics)
	}
}

// send makes a synchronous change and reads the answer into state.
func (r *associationResource) send(ctx context.Context, method, p string, body map[string]any, m *associationModel, state *tfsdk.State, d *diag.Diagnostics) {
	out, err := r.client.Do(ctx, service, method, p, body)
	var v associationView
	if err == nil {
		err = out.Decode(&v)
	}
	if err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	d.Append(state.Set(ctx, m)...)
}

func (r *associationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m associationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v associationView
	if readOrForget(ctx, r.client, m.ProjectID.ValueString(), r.path(m, m.ID.ValueString()), "kpa_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *associationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m associationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"bot": m.BotID.ValueString(), "tags": stringMap(ctx, m.Tags, &resp.Diagnostics)}
	if !resp.Diagnostics.HasError() {
		r.send(ctx, "PATCH", r.path(m, m.ID.ValueString()), body, &m, &resp.State, &resp.Diagnostics)
	}
}

func (r *associationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m associationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if _, err := r.client.Do(ctx, service, "DELETE", r.path(m, m.ID.ValueString()), nil); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *associationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(ctx, req, resp, r.client, []string{"cluster_id", "id"}, []string{"kcl_", "kpa_"})
}

func (m *associationModel) fromAPI(ctx context.Context, v associationView, org string) {
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.ClusterID, m.Namespace, m.ServiceAccount = types.StringValue(v.Cluster), types.StringValue(v.Namespace), types.StringValue(v.ServiceAccount)
	m.BotID, m.Tags = types.StringValue(v.Bot), mapValue(ctx, v.Tags)
}
