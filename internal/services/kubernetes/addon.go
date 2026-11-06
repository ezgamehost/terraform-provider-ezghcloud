package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const addonTimeout = "10m"

// clusterAddons are the add-ons a new cluster installs (kubernetes-stack addons/*/meta.json
// default: true): declaring one takes it over. Any other that exists was made elsewhere, and its
// addon_exists refusal stands.
var clusterAddons = []string{"vpc-cni", "kube-proxy", "coredns", "pod-identity-agent", "blockstore-csi"}

type addonModel struct {
	ID               types.String `tfsdk:"id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	ProjectID        types.String `tfsdk:"project_id"`
	ClusterID        types.String `tfsdk:"cluster_id"`
	ResourceName     types.String `tfsdk:"resource_name"`
	Name             types.String `tfsdk:"name"`
	Version          types.String `tfsdk:"version"`
	InstalledVersion types.String `tfsdk:"installed_version"`
	Configuration    types.String `tfsdk:"configuration"`
	Status           types.String `tfsdk:"status"`
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type addonView struct {
	Slug, ResourceName, Name, Cluster, Status, Version string
	Pinned                                             bool
	Configuration                                      json.RawMessage
}

type addonResource struct{ client *client.Client }

func (*addonResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_addon"
}
func (*addonResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A managed add-on in a cluster. Add-ons a new cluster installs (vpc-cni, kube-proxy, coredns, pod-identity-agent, blockstore-csi) are taken over when declared, and left installed on destroy if they can't be removed. Fields EZGH manages are overwritten if changed in the cluster.", Attributes: map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, Description: "Add-on ID (kad_).", PlanModifiers: keepString},
		"organization_id":   schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":        schema.StringAttribute{Required: true, Description: "The cluster's project. Changing it replaces the add-on.", PlanModifiers: replace},
		"cluster_id":        schema.StringAttribute{Required: true, Description: "Cluster (kcl_). Changing it replaces the add-on.", PlanModifiers: replace},
		"resource_name":     schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"name":              schema.StringAttribute{Required: true, Description: "Add-on name, such as coredns, files-csi, secrets-store, obs-agent or metrics-server. Changing it replaces the add-on.", PlanModifiers: replace},
		"version":           schema.StringAttribute{Optional: true, Description: "Pin the add-on to this version. Left out, it follows the cluster: each cluster upgrade moves it to the newest version for the new Kubernetes version."},
		"installed_version": schema.StringAttribute{Computed: true, Description: "The version the cluster runs."},
		"configuration":     schema.StringAttribute{Optional: true, Description: "The add-on's configuration, a JSON object checked against its version's schema. Compared as JSON, so formatting isn't drift. Left out, none."},
		"status":            schema.StringAttribute{Computed: true, Description: "What the add-on is doing: creating, active, updating, deleting or failed."},
		"operation_id":      schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(addonTimeout), Description: "Maximum wait for each asynchronous operation, for example 10m."},
	}}
}
func (r *addonResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// sameJSON reports whether a and b are the same JSON value, whatever their formatting.
func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

// ModifyPlan refuses a configuration that isn't a JSON object, and keeps state's text when the
// configured one is the same JSON.
func (r *addonResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	replaceFailed(ctx, req, resp)
	var plan addonModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !known(plan.Configuration) {
		return
	}
	var obj map[string]any
	if json.Unmarshal([]byte(plan.Configuration.ValueString()), &obj) != nil || obj == nil {
		resp.Diagnostics.AddAttributeError(path.Root("configuration"), "Invalid configuration", "configuration must be a JSON object, for example jsonencode({ replicaCount = 3 }).")
		return
	}
	if req.State.Raw.IsNull() {
		return
	}
	var state addonModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if known(state.Configuration) && sameJSON(plan.Configuration.ValueString(), state.Configuration.ValueString()) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("configuration"), state.Configuration)...)
	}
}

func (r *addonResource) path(m addonModel, id ...string) string {
	return clusterPath(r.client, m.ProjectID.ValueString(), m.ClusterID.ValueString(), append([]string{"addons"}, id...)...)
}

// body is what a create or update sends: a version pins, none follows the cluster.
func (m addonModel) body() map[string]any {
	b := map[string]any{"pinned": known(m.Version), "configuration": nil}
	if known(m.Version) {
		b["version"] = m.Version.ValueString()
	}
	if known(m.Configuration) {
		b["configuration"] = json.RawMessage(m.Configuration.ValueString())
	}
	return b
}

func (r *addonResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m addonModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, addonTimeout, &resp.Diagnostics)
	defer cancel()
	m.OrganizationID, m.Status = types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	body := m.body()
	body["name"] = m.Name.ValueString()
	out, err := r.client.Do(ctx, service, "POST", r.path(m), body)
	var e *client.Error
	if errors.As(err, &e) && e.StatusCode == 409 && e.Code == "addon_exists" && slices.Contains(clusterAddons, m.Name.ValueString()) {
		r.adopt(ctx, &m, &resp.State, &resp.Diagnostics)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.ID, m.OperationID = types.StringValue(id), operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if !resp.Diagnostics.HasError() && finish(ctx, r.client, out, id, m.OperationID.ValueString(), "Add-on", &resp.Diagnostics) {
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
	}
}

// adopt takes over an add-on the cluster already has (one every cluster gets), setting it as
// configured.
func (r *addonResource) adopt(ctx context.Context, m *addonModel, state *tfsdk.State, d *diag.Diagnostics) {
	rows, err := r.client.List(ctx, service, r.path(*m))
	if err != nil {
		apiError(d, err)
		return
	}
	for _, row := range rows {
		if row["name"] == m.Name.ValueString() {
			m.ID = types.StringValue(fmt.Sprint(row["slug"]))
		}
	}
	if !known(m.ID) {
		d.AddError("Add-on not found", "The cluster says it has "+m.Name.ValueString()+", but doesn't list it. Refresh and retry.")
		return
	}
	r.patch(ctx, m, state, d)
}

func (r *addonResource) patch(ctx context.Context, m *addonModel, state *tfsdk.State, d *diag.Diagnostics) {
	out, err := r.client.Do(ctx, service, "PATCH", r.path(*m, m.ID.ValueString()), m.body())
	if err != nil {
		apiError(d, err)
		return
	}
	m.OperationID = operationID(out)
	d.Append(setKnown(ctx, state, m)...)
	if !d.HasError() && finish(ctx, r.client, out, m.ID.ValueString(), m.OperationID.ValueString(), "Add-on", d) {
		r.refresh(ctx, m, state, d)
	}
}

func (r *addonResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m addonModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v addonView
	if readOrForget(ctx, r.client, m.ProjectID.ValueString(), r.path(m, m.ID.ValueString()), "kad_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if waitMoving(ctx, r.client, v.Status, m.OperationID, m.OperationTimeout, addonTimeout, &resp.Diagnostics) {
			r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
		}
	}
}

func (r *addonResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old addonModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, addonTimeout, &resp.Diagnostics)
	defer cancel()
	if m.Version.Equal(old.Version) && m.Configuration.Equal(old.Configuration) { // provider settings only
		m.OperationID = old.OperationID
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
		return
	}
	r.patch(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *addonResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m addonModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, addonTimeout, &resp.Diagnostics)
	defer cancel()
	err := remove(ctx, r.client, r.path(m, m.ID.ValueString()))
	var e *client.Error
	if errors.As(err, &e) && e.StatusCode == 409 && e.Code == "addon_not_removable" {
		resp.Diagnostics.AddWarning("Add-on left installed", m.Name.ValueString()+" is part of every cluster and stays installed, as last configured. It's no longer managed by Terraform.")
		return
	}
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *addonResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(ctx, req, resp, r.client, []string{"cluster_id", "id"}, []string{"kcl_", "kad_"})
}

func (r *addonResource) refresh(ctx context.Context, m *addonModel, state *tfsdk.State, d *diag.Diagnostics) {
	var v addonView
	if err := get(ctx, r.client, r.path(*m, m.ID.ValueString()), "kad_", &v); err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(v, r.client.OrganizationID)
	d.Append(state.Set(ctx, m)...)
}

// fromAPI reads the add-on into m. version is set only while it's pinned; configuration keeps its
// configured text while it's the same JSON.
func (m *addonModel) fromAPI(v addonView, org string) {
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.ClusterID, m.Name, m.InstalledVersion, m.Status = types.StringValue(v.Cluster), types.StringValue(v.Name), types.StringValue(v.Version), types.StringValue(v.Status)
	m.Version = types.StringNull()
	if v.Pinned {
		m.Version = types.StringValue(v.Version)
	}
	switch cfg := bytes.TrimSpace(v.Configuration); {
	case len(cfg) == 0 || string(cfg) == "null":
		m.Configuration = types.StringNull()
	case known(m.Configuration) && sameJSON(m.Configuration.ValueString(), string(cfg)):
	default:
		m.Configuration = types.StringValue(string(cfg))
	}
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(addonTimeout)
	}
}
