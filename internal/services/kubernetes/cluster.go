package kubernetes

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const clusterTimeout = "45m"

// clusterFields are what the API shows; the data source reads exactly these.
type clusterFields struct {
	ID                      types.String `tfsdk:"id"`
	OrganizationID          types.String `tfsdk:"organization_id"`
	ProjectID               types.String `tfsdk:"project_id"`
	ResourceName            types.String `tfsdk:"resource_name"`
	Name                    types.String `tfsdk:"name"`
	Version                 types.String `tfsdk:"version"`
	PatchVersion            types.String `tfsdk:"patch_version"`
	VPCID                   types.String `tfsdk:"vpc_id"`
	SubnetID                types.String `tfsdk:"subnet_id"`
	BotID                   types.String `tfsdk:"bot_id"`
	EncryptionKeyID         types.String `tfsdk:"encryption_key_id"`
	PublicAccessCIDRs       types.List   `tfsdk:"public_access_cidrs"`
	ServiceIPv4CIDR         types.String `tfsdk:"service_ipv4_cidr"`
	ServiceIPv6CIDR         types.String `tfsdk:"service_ipv6_cidr"`
	LoggingTypes            types.Set    `tfsdk:"logging_types"`
	LoggingProjectID        types.String `tfsdk:"logging_project_id"`
	ControlPlaneMetrics     types.Bool   `tfsdk:"control_plane_metrics"`
	MaintenanceDay          types.String `tfsdk:"maintenance_window_day"`
	MaintenanceStartHour    types.Int64  `tfsdk:"maintenance_window_start_hour"`
	Tags                    types.Map    `tfsdk:"tags"`
	Status                  types.String `tfsdk:"status"`
	EndpointHostname        types.String `tfsdk:"endpoint_hostname"`
	EndpointPrivateHostname types.String `tfsdk:"endpoint_private_hostname"`
	EndpointPublicIPv4      types.String `tfsdk:"endpoint_public_ipv4"`
	CertificateAuthority    types.String `tfsdk:"certificate_authority"`
	OIDCIssuer              types.String `tfsdk:"oidc_issuer"`
	NodeFirewallID          types.String `tfsdk:"node_firewall_id"`
}
type clusterModel struct {
	clusterFields
	BootstrapCreatorAccess types.Bool   `tfsdk:"bootstrap_creator_access"`
	OperationID            types.String `tfsdk:"operation_id"`
	OperationTimeout       types.String `tfsdk:"operation_timeout"`
}
type clusterView struct {
	Slug, ResourceName, Name, Status, Version, VPC, Subnet, Bot, ServiceIpv4Cidr, ServiceIpv6Cidr, OidcIssuer string
	PatchVersion, EncryptionKey, CertificateAuthority, NodeFirewall                                           *string
	Endpoint                                                                                                  struct {
		Hostname, PrivateHostname string
		PublicIpv4                *string
		PublicAccessCidrs         []string
	}
	Logging struct {
		Types      []string
		ObsProject *string
	}
	ControlPlaneMetrics bool
	MaintenanceWindow   struct {
		Day       string
		StartHour int64
	}
	Tags map[string]string
}

type clusterResource struct{ client *client.Client }

func (*clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

const fixedAtCreate = "It can't change after create: replacing a cluster would lose its workloads, so a change is refused at plan."

func (*clusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string, keep bool) schema.StringAttribute {
		a := schema.StringAttribute{Computed: true, Description: d}
		if keep {
			a.PlanModifiers = keepString
		}
		return a
	}
	resp.Schema = schema.Schema{Description: "An EZGH Kubernetes cluster: a Kubernetes control plane EZGH runs, reached at one endpoint, in your VPC. Its nodes come from ezghcloud_kubernetes_node_group. A cluster with node groups can't be destroyed: destroy them first.", Attributes: map[string]schema.Attribute{
		"id":                     computed("Cluster ID (kcl_).", true),
		"organization_id":        computed("Organization managed by the configured provider.", true),
		"project_id":             schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the cluster.", PlanModifiers: replace},
		"resource_name":          computed("IAM resource name.", true),
		"name":                   schema.StringAttribute{Required: true, Description: "Cluster name. Changing it replaces the cluster.", PlanModifiers: replace},
		"version":                schema.StringAttribute{Optional: true, Computed: true, Description: "Kubernetes minor version, such as 1.37. Left out, the newest offered. Upgrades in place one minor version at a time; a downgrade is refused at plan. Node groups keep their version until each is upgraded.", PlanModifiers: keepString},
		"patch_version":          computed("The patch release the control plane runs; EZGH updates it in the weekly maintenance window.", false),
		"vpc_id":                 computed("VPC of the subnet.", true),
		"subnet_id":              schema.StringAttribute{Required: true, Description: "Subnet (sbn_) the endpoint's private addresses live in. Its route table must send 0.0.0.0/0 to a Network Gateway. " + fixedAtCreate},
		"bot_id":                 schema.StringAttribute{Required: true, Description: "Cluster Bot (bot_): load balancers, volumes and node capacity the cluster makes are made as this Bot. Needs KubernetesClusterAccess, and the caller needs bots.pass on it. " + fixedAtCreate},
		"encryption_key_id":      schema.StringAttribute{Optional: true, Description: "Keys key (kms_) Kubernetes Secrets are encrypted with. Left out, an EZGH-managed key. " + fixedAtCreate},
		"public_access_cidrs":    schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "IPv4 CIDRs allowed to reach the public endpoint, at most 40. Left out at create, 0.0.0.0/0; removed later, the last value stays. An empty list closes the public endpoint: kubectl then reaches the cluster only from inside the VPC.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"service_ipv4_cidr":      schema.StringAttribute{Optional: true, Computed: true, Description: "IPv4 range Services get addresses from. Left out, 10.100.0.0/16. " + fixedAtCreate, PlanModifiers: keepString},
		"service_ipv6_cidr":      computed("IPv6 range Services get addresses from.", true),
		"logging_types":          schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), Description: "Control-plane log types sent to logging_project_id's Obs: api (kube-apiserver's logs), audit (kube-apiserver's audit log), authenticator (one entry per sign-in: who and whether it succeeded, never the token), controllerManager and scheduler. Billed as that project's log ingest. Changes in place."},
		"logging_project_id":     schema.StringAttribute{Optional: true, Description: "Project (prj_) whose Obs receives the control-plane logs, billed as its log ingest. Required with logging_types."},
		"control_plane_metrics":  schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "When true, the control plane's apiserver, etcd, scheduler and controller-manager metrics go to the cluster's own project in Obs once a minute, free: an allowlisted set, at most 2,000 series per control-plane member a minute. Default false. Changes in place."},
		"maintenance_window_day": schema.StringAttribute{Optional: true, Computed: true, Description: "Day of the weekly 4-hour maintenance window, monday to sunday. Set with maintenance_window_start_hour; left out at create, EZGH picks one; removed later, the last window stays.", PlanModifiers: keepString},
		"maintenance_window_start_hour": schema.Int64Attribute{Optional: true, Computed: true, Description: "Start hour (UTC, 0 to 23) of the maintenance window.",
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"tags":                      schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"status":                    computed("What the cluster is doing: creating, active, updating, deleting or failed.", false),
		"endpoint_hostname":         computed("The endpoint's public DNS name, for kubectl.", true),
		"endpoint_private_hostname": computed("The endpoint's DNS name inside the VPC.", true),
		"endpoint_public_ipv4":      computed("The endpoint's public IPv4 address; null until assigned.", true),
		"certificate_authority":     computed("The cluster CA certificate, PEM; null until the cluster is active.", true),
		"oidc_issuer":               computed("The cluster's service account token issuer, for other OIDC relying parties.", true),
		"node_firewall_id":          computed("Firewall (fwl_) the cluster's nodes are in.", true),
		"bootstrap_creator_access":  schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Give the caller who creates the cluster an access entry with cluster admin. " + fixedAtCreate},
		"operation_id":              computed("Most recent operation initiated by this resource; unavailable on import.", false),
		"operation_timeout":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(clusterTimeout), Description: "Maximum wait for each asynchronous operation, for example 45m."},
	}}
}
func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// minor is 37 for "1.37", else -1.
func minor(v string) int {
	major, m, ok := strings.Cut(v, ".")
	n, err := strconv.Atoi(m)
	if !ok || major != "1" || err != nil {
		return -1
	}
	return n
}

func (r *clusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An unknown value counts as a change: a replaced subnet, Bot or key.
	for name, pair := range map[string][2]attr.Value{"subnet_id": {plan.SubnetID, state.SubnetID}, "bot_id": {plan.BotID, state.BotID},
		"encryption_key_id": {plan.EncryptionKeyID, state.EncryptionKeyID}, "service_ipv4_cidr": {plan.ServiceIPv4CIDR, state.ServiceIPv4CIDR},
		"bootstrap_creator_access": {plan.BootstrapCreatorAccess, state.BootstrapCreatorAccess}} {
		if name == "bootstrap_creator_access" && pair[1].IsNull() {
			continue // imported: the API doesn't show it, so whatever the config says is kept
		}
		if !pair[0].Equal(pair[1]) {
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" can't change",
				fmt.Sprintf("%s is fixed when the cluster is created, and replacing the cluster would lose its workloads. To change it, create a new cluster and move the workloads.", name))
		}
	}
	if known(plan.Version) && !plan.Version.Equal(state.Version) {
		from, to := minor(state.Version.ValueString()), minor(plan.Version.ValueString())
		if to != from+1 {
			resp.Diagnostics.AddAttributeError(path.Root("version"), "Upgrade one minor version at a time",
				fmt.Sprintf("A cluster upgrades one minor version at a time and is never downgraded: from %s, set version to 1.%d.", state.Version.ValueString(), from+1))
		}
	}
	if known(plan.MaintenanceDay) != known(plan.MaintenanceStartHour) && !plan.MaintenanceDay.IsUnknown() && !plan.MaintenanceStartHour.IsUnknown() {
		resp.Diagnostics.AddError("Incomplete maintenance window", "Set maintenance_window_day and maintenance_window_start_hour together.")
	}
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, clusterTimeout, &resp.Diagnostics)
	defer cancel()
	body := map[string]any{"name": m.Name.ValueString(), "subnet": m.SubnetID.ValueString(), "bot": m.BotID.ValueString(),
		"bootstrapCreatorAccess": m.BootstrapCreatorAccess.ValueBool(), "tags": stringMap(ctx, m.Tags, &resp.Diagnostics)}
	if known(m.Version) {
		body["version"] = m.Version.ValueString()
	}
	if known(m.EncryptionKeyID) {
		body["encryptionKey"] = m.EncryptionKeyID.ValueString()
	}
	if known(m.ServiceIPv4CIDR) {
		body["serviceIpv4Cidr"] = m.ServiceIPv4CIDR.ValueString()
	}
	if known(m.PublicAccessCIDRs) {
		body["publicAccessCidrs"] = stringsOf(ctx, m.PublicAccessCIDRs, &resp.Diagnostics)
	}
	body["logging"] = loggingBody(ctx, m, &resp.Diagnostics)
	body["controlPlaneMetrics"] = m.ControlPlaneMetrics.ValueBool()
	if known(m.MaintenanceDay) && known(m.MaintenanceStartHour) {
		body["maintenanceWindow"] = map[string]any{"day": m.MaintenanceDay.ValueString(), "startHour": m.MaintenanceStartHour.ValueInt64()}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID, m.Status = types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	if !startAsync(ctx, r.client, client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "clusters"), body, &m.ID, &m.OperationID, &resp.State, &m, "Cluster", &resp.Diagnostics) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func loggingBody(ctx context.Context, m clusterModel, d *diag.Diagnostics) map[string]any {
	return map[string]any{"types": stringsOf(ctx, m.LoggingTypes, d), "obsProject": m.LoggingProjectID.ValueStringPointer()}
}

func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v clusterView
	if !readOrForget(ctx, r.client, m.ProjectID.ValueString(), clusterPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), "kcl_", &v, &resp.State, &resp.Diagnostics) {
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if waitMoving(ctx, r.client, v.Status, m.OperationID, m.OperationTimeout, clusterTimeout, &resp.Diagnostics) {
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
	}
}

func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, clusterTimeout, &resp.Diagnostics)
	defer cancel()
	body := map[string]any{}
	if known(m.Version) && !m.Version.Equal(old.Version) {
		body["version"] = m.Version.ValueString()
	}
	if known(m.PublicAccessCIDRs) && !m.PublicAccessCIDRs.Equal(old.PublicAccessCIDRs) {
		body["publicAccessCidrs"] = stringsOf(ctx, m.PublicAccessCIDRs, &resp.Diagnostics)
	}
	if !m.LoggingTypes.Equal(old.LoggingTypes) || !m.LoggingProjectID.Equal(old.LoggingProjectID) {
		body["logging"] = loggingBody(ctx, m, &resp.Diagnostics)
	}
	if known(m.ControlPlaneMetrics) && !m.ControlPlaneMetrics.Equal(old.ControlPlaneMetrics) {
		body["controlPlaneMetrics"] = m.ControlPlaneMetrics.ValueBool()
	}
	if known(m.MaintenanceDay) && known(m.MaintenanceStartHour) && (!m.MaintenanceDay.Equal(old.MaintenanceDay) || !m.MaintenanceStartHour.Equal(old.MaintenanceStartHour)) {
		body["maintenanceWindow"] = map[string]any{"day": m.MaintenanceDay.ValueString(), "startHour": m.MaintenanceStartHour.ValueInt64()}
	}
	if !m.Tags.Equal(old.Tags) {
		body["tags"] = stringMap(ctx, m.Tags, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if len(body) == 0 { // only provider settings changed
		m.OperationID = old.OperationID
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
		return
	}
	p := clusterPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	out, err := r.client.Do(ctx, service, "PATCH", p, body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if !finish(ctx, r.client, out, m.ID.ValueString(), m.OperationID.ValueString(), "Cluster", &resp.Diagnostics) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, clusterTimeout, &resp.Diagnostics)
	defer cancel()
	if err := remove(ctx, r.client, clusterPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString())); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(ctx, req, resp, r.client, []string{"id"}, []string{"kcl_"})
}

// refresh reads the cluster into m and state after an operation.
func (r *clusterResource) refresh(ctx context.Context, m *clusterModel, state *tfsdk.State, d *diag.Diagnostics) {
	var v clusterView
	if err := get(ctx, r.client, clusterPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), "kcl_", &v); err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	d.Append(state.Set(ctx, m)...)
}

func (f *clusterFields) fill(ctx context.Context, v clusterView, org string) {
	f.ID, f.OrganizationID, f.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	f.Name, f.Version, f.PatchVersion = types.StringValue(v.Name), types.StringValue(v.Version), types.StringPointerValue(v.PatchVersion)
	f.VPCID, f.SubnetID, f.BotID = types.StringValue(v.VPC), types.StringValue(v.Subnet), types.StringValue(v.Bot)
	f.EncryptionKeyID = types.StringPointerValue(v.EncryptionKey)
	f.PublicAccessCIDRs = listValue(ctx, v.Endpoint.PublicAccessCidrs)
	f.ServiceIPv4CIDR, f.ServiceIPv6CIDR = types.StringValue(v.ServiceIpv4Cidr), types.StringValue(v.ServiceIpv6Cidr)
	f.LoggingTypes, f.LoggingProjectID = setValue(ctx, v.Logging.Types), types.StringPointerValue(v.Logging.ObsProject)
	f.ControlPlaneMetrics = types.BoolValue(v.ControlPlaneMetrics)
	f.MaintenanceDay, f.MaintenanceStartHour = types.StringValue(v.MaintenanceWindow.Day), types.Int64Value(v.MaintenanceWindow.StartHour)
	f.Tags, f.Status = mapValue(ctx, v.Tags), types.StringValue(v.Status)
	f.EndpointHostname, f.EndpointPrivateHostname = types.StringValue(v.Endpoint.Hostname), types.StringValue(v.Endpoint.PrivateHostname)
	f.EndpointPublicIPv4, f.CertificateAuthority = types.StringPointerValue(v.Endpoint.PublicIpv4), types.StringPointerValue(v.CertificateAuthority)
	f.OIDCIssuer, f.NodeFirewallID = types.StringValue(v.OidcIssuer), types.StringPointerValue(v.NodeFirewall)
}

// fromAPI reads the cluster into the resource; what the API doesn't show keeps its planned value.
func (m *clusterModel) fromAPI(ctx context.Context, v clusterView, org string) {
	m.fill(ctx, v, org)
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(clusterTimeout)
	}
}
