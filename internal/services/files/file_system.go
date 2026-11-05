package files

import (
	"context"
	"fmt"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type fileSystemModel struct {
	common
	Description          types.String `tfsdk:"description"`
	VPCID                types.String `tfsdk:"vpc_id"`
	RootSquash           types.Bool   `tfsdk:"root_squash"`
	RequireTLS           types.Bool   `tfsdk:"require_tls"`
	DeletionProtection   types.Bool   `tfsdk:"deletion_protection"`
	SizeBytes            types.String `tfsdk:"size_bytes"`
	ThroughputMBps       types.Int64  `tfsdk:"throughput_mbps"`
	MetadataOpsPerSecond types.Int64  `tfsdk:"metadata_ops_per_second"`
}
type fileSystemView struct {
	view
	Description                                *string
	VPC, SizeBytes                             string
	RootSquash, RequireTLS, DeletionProtection bool
	Limits                                     struct{ ThroughputMBps, MetadataOpsPerSecond int64 }
}

type fileSystemResource struct{ client *client.Client }

func (*fileSystemResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_file_system"
}
func (*fileSystemResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := commonAttributes("File system", "fsy_")
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	a["project_id"] = schema.StringAttribute{Required: true, Description: "Project identifier. It can't change: a replacement would destroy the file system's files, so a change is refused at plan."}
	a["description"] = schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."}
	a["vpc_id"] = schema.StringAttribute{Required: true, Description: "VPC (vpc_) whose subnets can hold its mount targets. It can't change: a replacement would destroy the file system's files, so a change is refused at plan."}
	a["root_squash"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Map a client's root user to an unprivileged user. Defaults to true."}
	a["require_tls"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Accept only mounts with TLS (xprtsec=tls). Defaults to false."}
	a["deletion_protection"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "While true, the file system can't be destroyed."}
	a["size_bytes"] = schema.StringAttribute{Computed: true, Description: "Bytes stored, as a decimal string, measured about once an hour."}
	a["throughput_mbps"] = schema.Int64Attribute{Computed: true, Description: "Throughput limit in MB/s.", PlanModifiers: keepInt}
	a["metadata_ops_per_second"] = schema.Int64Attribute{Computed: true, Description: "Metadata operations per second limit.", PlanModifiers: keepInt}
	resp.Schema = schema.Schema{Description: "An elastic NFS file system in one VPC, mounted through its mount targets. Destroying it deletes its files; delete its mount targets and access points first.", Attributes: a}
}
func (r *fileSystemResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ModifyPlan refuses a change that would replace the file system and destroy its files. An unknown
// value counts as a change: a replaced VPC or project.
func (r *fileSystemResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	for _, name := range []string{"project_id", "vpc_id"} {
		var plan, state types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(name), &plan)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &state)...)
		if !plan.Equal(state) {
			to := "a value known only at apply"
			if known(plan) {
				to = plan.ValueString()
			}
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" can't change",
				fmt.Sprintf("Changing %s from %s to %s would replace the file system and destroy its files. Create a new file system, copy the files, then remove this one.", name, state.ValueString(), to))
		}
	}
}

func (r *fileSystemResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m fileSystemModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics), "vpc": m.VPCID.ValueString(),
		"rootSquash": m.RootSquash.ValueBool(), "requireTls": m.RequireTLS.ValueBool(), "deletionProtection": m.DeletionProtection.ValueBool()}
	if known(m.Description) {
		body["description"] = m.Description.ValueString()
	}
	if resp.Diagnostics.HasError() || !create(ctx, r.client, "file-systems", &m.common, &m, &resp.State, body, &resp.Diagnostics) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *fileSystemResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m fileSystemModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v fileSystemView
	if readState(ctx, r.client, &m.common, "file-systems", "fsy_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// Update is synchronous: the API answers with the changed file system.
func (r *fileSystemResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m fileSystemModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics),
		"rootSquash": m.RootSquash.ValueBool(), "requireTls": m.RequireTLS.ValueBool(), "deletionProtection": m.DeletionProtection.ValueBool()}
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", itemPath(r.client, m.ProjectID.ValueString(), "file-systems", m.ID.ValueString()), body)
	var v fileSystemView
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
func (r *fileSystemResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m fileSystemModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("File system deletion protection enabled", "Set deletion_protection to false and apply before destroying this file system.")
		return
	}
	remove(ctx, r.client, "file-systems", &m.common, &resp.Diagnostics)
}
func (r *fileSystemResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "fsy_")
}

func (r *fileSystemResource) refresh(ctx context.Context, m *fileSystemModel, state *tfsdk.State, diags *diag.Diagnostics) {
	var v fileSystemView
	if err := read(ctx, r.client, m.ProjectID.ValueString(), "file-systems", m.ID.ValueString(), "fsy_", &v); err != nil {
		apiError(diags, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	diags.Append(state.Set(ctx, m)...)
}

func (m *fileSystemModel) fromAPI(ctx context.Context, v fileSystemView, org string) {
	description := m.Description
	m.fill(ctx, v.view, org)
	m.Description = types.StringPointerValue(v.Description)
	if description.ValueString() == "" && known(description) && v.Description == nil {
		m.Description = description // the API shows an empty description as null
	}
	m.VPCID = types.StringValue(v.VPC)
	m.RootSquash, m.RequireTLS, m.DeletionProtection = types.BoolValue(v.RootSquash), types.BoolValue(v.RequireTLS), types.BoolValue(v.DeletionProtection)
	m.SizeBytes = types.StringValue(v.SizeBytes)
	m.ThroughputMBps, m.MetadataOpsPerSecond = types.Int64Value(v.Limits.ThroughputMBps), types.Int64Value(v.Limits.MetadataOpsPerSecond)
}
