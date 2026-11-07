package kubernetes

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type accessPolicy struct {
	Policy     types.String `tfsdk:"policy"`
	Namespaces types.List   `tfsdk:"namespaces"`
}

var accessPolicyType = types.ObjectType{AttrTypes: map[string]attr.Type{"policy": types.StringType, "namespaces": types.ListType{ElemType: types.StringType}}}

type accessEntryModel struct {
	ID               types.String `tfsdk:"id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	ProjectID        types.String `tfsdk:"project_id"`
	ClusterID        types.String `tfsdk:"cluster_id"`
	ResourceName     types.String `tfsdk:"resource_name"`
	PrincipalType    types.String `tfsdk:"principal_type"`
	PrincipalID      types.String `tfsdk:"principal_id"`
	Username         types.String `tfsdk:"username"`
	KubernetesGroups types.List   `tfsdk:"kubernetes_groups"`
	AccessPolicies   types.List   `tfsdk:"access_policy"`
}
type accessEntryView struct {
	Slug, ResourceName, Cluster, Username string
	Principal                             struct{ Type, ID string }
	KubernetesGroups                      []string
	AccessPolicies                        []struct {
		Policy     string
		Namespaces []string
	}
}

type accessEntryResource struct{ client *client.Client }

func (*accessEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_access_entry"
}
func (*accessEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "An access entry: an EZGH principal's identity in a cluster, its Kubernetes groups and access policies. Each kubectl request is checked against IAM as it's made; the principal also needs kubernetes.clusters.accessKubernetesApi.", Attributes: map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, Description: "Access entry ID (kae_).", PlanModifiers: keepString},
		"organization_id":   schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":        schema.StringAttribute{Required: true, Description: "The cluster's project. Changing it replaces the entry.", PlanModifiers: replace},
		"cluster_id":        schema.StringAttribute{Required: true, Description: "Cluster (kcl_). Changing it replaces the entry.", PlanModifiers: replace},
		"resource_name":     schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"principal_type":    schema.StringAttribute{Required: true, Description: "user, bot, ssoUser or group. Changing it replaces the entry.", PlanModifiers: replace},
		"principal_id":      schema.StringAttribute{Required: true, Description: "The principal's ID: a user's own ID, a Bot's bot_ ID, an SSO user's usr_ ID or a group's grp_ ID. Changing it replaces the entry.", PlanModifiers: replace},
		"username":          schema.StringAttribute{Computed: true, Description: "The principal's Kubernetes user name, ezgh:<principal id>.", PlanModifiers: keepString},
		"kubernetes_groups": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})), Description: "Kubernetes groups the principal is in, for your own RBAC bindings. system: and ezgh: names are reserved."},
	}, Blocks: map[string]schema.Block{
		"access_policy": schema.ListNestedBlock{Description: "An access policy: clusterAdmin, admin, edit or view, for the whole cluster or listed namespaces.", NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
			"policy":     schema.StringAttribute{Required: true, Description: "clusterAdmin, admin, edit or view."},
			"namespaces": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Namespaces it applies in. Left out, the whole cluster (clusterAdmin always is)."},
		}}},
	}}
}
func (r *accessEntryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ValidateConfig refuses clusterAdmin for listed namespaces, as the API does: it's for the whole
// cluster.
func (r *accessEntryResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var policies types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("access_policy"), &policies)...)
	var ps []accessPolicy
	if resp.Diagnostics.HasError() || !known(policies) || policies.ElementsAs(ctx, &ps, false).HasError() {
		return
	}
	for i, p := range ps {
		if p.Policy.ValueString() == "clusterAdmin" && known(p.Namespaces) && len(p.Namespaces.Elements()) > 0 {
			resp.Diagnostics.AddAttributeError(path.Root("access_policy").AtListIndex(i).AtName("namespaces"), "clusterAdmin is for the whole cluster",
				"Leave namespaces out for clusterAdmin, or use admin for listed namespaces.")
		}
	}
}

func (r *accessEntryResource) path(m accessEntryModel, id ...string) string {
	return clusterPath(r.client, m.ProjectID.ValueString(), m.ClusterID.ValueString(), append([]string{"access-entries"}, id...)...)
}

// policiesBody is accessPolicies: namespaces null for the whole cluster.
func policiesBody(ctx context.Context, v types.List, d *diag.Diagnostics) []map[string]any {
	var ps []accessPolicy
	if known(v) {
		d.Append(v.ElementsAs(ctx, &ps, false)...)
	}
	out := []map[string]any{}
	for _, p := range ps {
		var namespaces []string // null: the whole cluster
		if known(p.Namespaces) && len(p.Namespaces.Elements()) > 0 {
			namespaces = stringsOf(ctx, p.Namespaces, d)
		}
		out = append(out, map[string]any{"policy": p.Policy.ValueString(), "namespaces": namespaces})
	}
	return out
}

func (r *accessEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m accessEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"principal": map[string]any{"type": m.PrincipalType.ValueString(), "id": m.PrincipalID.ValueString()},
		"kubernetesGroups": stringsOf(ctx, m.KubernetesGroups, &resp.Diagnostics), "accessPolicies": policiesBody(ctx, m.AccessPolicies, &resp.Diagnostics)}
	if resp.Diagnostics.HasError() {
		return
	}
	r.send(ctx, "POST", r.path(m), body, &m, &resp.State, &resp.Diagnostics)
}

// send makes a synchronous change and reads the answer into state.
func (r *accessEntryResource) send(ctx context.Context, method, p string, body map[string]any, m *accessEntryModel, state *tfsdk.State, d *diag.Diagnostics) {
	out, err := r.client.Do(ctx, service, method, p, body)
	var v accessEntryView
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

func (r *accessEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m accessEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v accessEntryView
	if readOrForget(ctx, r.client, m.ProjectID.ValueString(), r.path(m, m.ID.ValueString()), "kae_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *accessEntryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m accessEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"kubernetesGroups": stringsOf(ctx, m.KubernetesGroups, &resp.Diagnostics), "accessPolicies": policiesBody(ctx, m.AccessPolicies, &resp.Diagnostics)}
	if !resp.Diagnostics.HasError() {
		r.send(ctx, "PATCH", r.path(m, m.ID.ValueString()), body, &m, &resp.State, &resp.Diagnostics)
	}
}

func (r *accessEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m accessEntryModel
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
func (r *accessEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(ctx, req, resp, r.client, []string{"cluster_id", "id"}, []string{"kcl_", "kae_"})
}

// fromAPI reads the entry into m. A cluster-wide policy written as namespaces = [] keeps [], so it
// isn't drift against the API's null.
func (m *accessEntryModel) fromAPI(ctx context.Context, v accessEntryView, org string) {
	var prior []accessPolicy
	if known(m.AccessPolicies) {
		_ = m.AccessPolicies.ElementsAs(ctx, &prior, false)
	}
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.ClusterID, m.Username = types.StringValue(v.Cluster), types.StringValue(v.Username)
	m.PrincipalType, m.PrincipalID = types.StringValue(v.Principal.Type), types.StringValue(v.Principal.ID)
	m.KubernetesGroups = listValue(ctx, v.KubernetesGroups)
	ps := []accessPolicy{}
	for i, p := range v.AccessPolicies {
		namespaces := types.ListNull(types.StringType)
		if p.Namespaces != nil {
			namespaces = listValue(ctx, p.Namespaces)
		} else if i < len(prior) && known(prior[i].Namespaces) && len(prior[i].Namespaces.Elements()) == 0 {
			namespaces = prior[i].Namespaces
		}
		ps = append(ps, accessPolicy{Policy: types.StringValue(p.Policy), Namespaces: namespaces})
	}
	m.AccessPolicies, _ = types.ListValueFrom(ctx, accessPolicyType, ps)
}
