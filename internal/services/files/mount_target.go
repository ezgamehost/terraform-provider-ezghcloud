package files

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type mountTargetModel struct {
	common
	FileSystemID       types.String `tfsdk:"file_system_id"`
	SubnetID           types.String `tfsdk:"subnet_id"`
	VPCID              types.String `tfsdk:"vpc_id"`
	AllowedSourceCIDRs types.List   `tfsdk:"allowed_source_cidrs"`
	Hostname           types.String `tfsdk:"hostname"`
	IPAddress          types.String `tfsdk:"ip_address"`
}
type mountTargetView struct {
	view
	FileSystem, Subnet, VPC string
	AllowedSourceCidrs      []string
	Hostname, IPv4Address   *string
}

type mountTargetResource struct{ client *client.Client }

func (*mountTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mount_target"
}
func (*mountTargetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := commonAttributes("Mount target", "fmt_")
	a["name"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Name, 1 to 100 characters. Defaults to the mount target's ID.", PlanModifiers: keepString}
	a["file_system_id"] = schema.StringAttribute{Required: true, Description: "File system (fsy_) it serves. Changing it replaces the mount target.", PlanModifiers: replace}
	a["subnet_id"] = schema.StringAttribute{Required: true, Description: "Subnet (sbn_) in the file system's VPC it gets its address in; one mount target per subnet per file system. Changing it replaces the mount target.", PlanModifiers: replace}
	a["vpc_id"] = schema.StringAttribute{Computed: true, Description: "VPC of the subnet.", PlanModifiers: keepString}
	a["allowed_source_cidrs"] = schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "IPv4 CIDRs allowed to mount. Left out, the VPC's ranges. Changes in place.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}}
	a["hostname"] = schema.StringAttribute{Computed: true, Description: "DNS name to mount, as <hostname>:/<file system or access point ID>.", PlanModifiers: keepString}
	a["ip_address"] = schema.StringAttribute{Computed: true, Description: "IPv4 address in the subnet.", PlanModifiers: keepString}
	resp.Schema = schema.Schema{Description: "A file system's NFS endpoint in one subnet, reachable from the whole VPC. Most file systems need one.", Attributes: a}
}
func (r *mountTargetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *mountTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m mountTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"fileSystem": m.FileSystemID.ValueString(), "subnet": m.SubnetID.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if known(m.Name) {
		body["name"] = m.Name.ValueString()
	}
	if known(m.AllowedSourceCIDRs) {
		cidrs := []string{}
		resp.Diagnostics.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if resp.Diagnostics.HasError() || !create(ctx, r.client, "mount-targets", &m.common, &m, &resp.State, body, &resp.Diagnostics) {
		return
	}
	var v mountTargetView
	if err := read(ctx, r.client, m.ProjectID.ValueString(), "mount-targets", m.ID.ValueString(), "fmt_", &v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *mountTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m mountTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v mountTargetView
	if readState(ctx, r.client, &m.common, "mount-targets", "fmt_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// Update is synchronous. allowed_source_cidrs goes only when it changed: the API takes it only
// while the mount target is available.
func (r *mountTargetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old mountTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if !m.AllowedSourceCIDRs.Equal(old.AllowedSourceCIDRs) {
		cidrs := []string{}
		resp.Diagnostics.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", itemPath(r.client, m.ProjectID.ValueString(), "mount-targets", m.ID.ValueString()), body)
	var v mountTargetView
	if err == nil {
		err = out.Decode(&v)
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *mountTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m mountTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	remove(ctx, r.client, "mount-targets", &m.common, &resp.Diagnostics)
}
func (r *mountTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "fmt_")
}

func (m *mountTargetModel) fromAPI(ctx context.Context, v mountTargetView, org string) {
	m.fill(ctx, v.view, org)
	m.FileSystemID, m.SubnetID, m.VPCID = types.StringValue(v.FileSystem), types.StringValue(v.Subnet), types.StringValue(v.VPC)
	if v.AllowedSourceCidrs == nil {
		v.AllowedSourceCidrs = []string{}
	}
	m.AllowedSourceCIDRs, _ = types.ListValueFrom(ctx, types.StringType, v.AllowedSourceCidrs)
	m.Hostname, m.IPAddress = types.StringPointerValue(v.Hostname), types.StringPointerValue(v.IPv4Address)
}
