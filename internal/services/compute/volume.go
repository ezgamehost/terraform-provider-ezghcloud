package compute

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type volumeResource struct{ client *client.Client }
type volumeModel struct {
	ID                 types.String `tfsdk:"id"`
	OrganizationID     types.String `tfsdk:"organization_id"`
	ProjectID          types.String `tfsdk:"project_id"`
	Name               types.String `tfsdk:"name"`
	VolumeType         types.String `tfsdk:"volume_type"`
	SizeBytes          types.String `tfsdk:"size_bytes"`
	IOPS               types.Int64  `tfsdk:"iops"`
	ThroughputMBps     types.Int64  `tfsdk:"throughput_mbps"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	Tags               types.Map    `tfsdk:"tags"`
	Status             types.String `tfsdk:"status"`
	OperationID        types.String `tfsdk:"operation_id"`
	OperationTimeout   types.String `tfsdk:"operation_timeout"`
}
type volumeView struct {
	ID, Slug, Name, VolumeType, SizeBytes, Status string
	IOPS                                          int64
	ThroughputMBps                                int64
	DeletionProtection                            bool
	Tags                                          map[string]string
}

func (*volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}
func (*volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "An independently managed empty block volume. Size is an exact decimal byte count and can grow in place.", Attributes: map[string]schema.Attribute{
		"id": computedID(), "organization_id": organizationAttribute(), "project_id": projectAttribute(), "name": schema.StringAttribute{Required: true},
		"volume_type":         schema.StringAttribute{Required: true, Description: "Catalog volume type. A type change replaces the volume.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"size_bytes":          schema.StringAttribute{Required: true, Description: "Exact positive decimal size in bytes. Shrinking is refused."},
		"iops":                schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"throughput_mbps":     schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
		"tags":                tagsAttribute(), "status": schema.StringAttribute{Computed: true}, "operation_id": schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."}, "operation_timeout": timeoutAttribute(),
	}}
}
func timeoutAttribute() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("30m"), Description: "Maximum wait for each asynchronous operation, for example 30m."}
}
func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req, &resp.Diagnostics, &r.client)
}
func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if err := validateSizeBytes(m.SizeBytes.ValueString()); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "volumeType": m.VolumeType.ValueString(), "sizeBytes": m.SizeBytes.ValueString(), "source": map[string]any{"kind": "empty"}, "tags": tags, "deletionProtection": m.DeletionProtection.ValueBool()}
	if !m.IOPS.IsUnknown() && !m.IOPS.IsNull() {
		body["iops"] = m.IOPS.ValueInt64()
	}
	if !m.ThroughputMBps.IsUnknown() && !m.ThroughputMBps.IsNull() {
		body["throughputMBps"] = m.ThroughputMBps.ValueInt64()
	}
	out, err := r.client.Do(ctx, "blockstore", "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "volumes"), body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body, "volume")
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.ID = types.StringValue(id)
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	m.Status = types.StringValue("creating")
	m.OperationID = operationID(out)
	if m.IOPS.IsUnknown() {
		m.IOPS = types.Int64Null()
	}
	if m.ThroughputMBps.IsUnknown() {
		m.ThroughputMBps = types.Int64Null()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = waitResponse(ctx, r.client, "blockstore", out); err != nil {
		apiError(&resp.Diagnostics, fmt.Errorf("volume %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	v, err := readVolume(ctx, r.client, m.ProjectID.ValueString(), id)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readVolume(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if v.Status == "creating" || v.Status == "deleting" {
		waitCtx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
		defer cancel()
		if resp.Diagnostics.HasError() {
			return
		}
		if m.OperationID.ValueString() == "" {
			resp.Diagnostics.AddWarning("Volume operation in progress", "The volume is transitional and its operation ID is unavailable. Retry refresh after the operation completes.")
			return
		}
		if err = r.client.Wait(waitCtx, "blockstore", client.OrgPath(r.client.OrganizationID, "operations", m.OperationID.ValueString())); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		v, err = readVolume(waitCtx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if v.Status == "creating" || v.Status == "deleting" {
			resp.Diagnostics.AddWarning("Volume operation in progress", "The volume is still transitional. Retry refresh after its current operation completes.")
		}
	}
}
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if err := validateGrowth(old.SizeBytes.ValueString(), m.SizeBytes.ValueString()); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	path := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "volumes", m.ID.ValueString())
	// Metadata and protection are separate from asynchronous performance changes.
	_, err := r.client.Do(ctx, "blockstore", "PATCH", path, map[string]any{"name": m.Name.ValueString(), "tags": tags, "deletionProtection": m.DeletionProtection.ValueBool()})
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	if !m.SizeBytes.Equal(old.SizeBytes) || !m.IOPS.Equal(old.IOPS) || !m.ThroughputMBps.Equal(old.ThroughputMBps) {
		body := map[string]any{"sizeBytes": m.SizeBytes.ValueString(), "iops": m.IOPS.ValueInt64(), "throughputMBps": m.ThroughputMBps.ValueInt64()}
		out, e := r.client.Do(ctx, "blockstore", "POST", path+"/modify", body)
		if e != nil {
			apiError(&resp.Diagnostics, e)
			return
		}
		m.OperationID = operationID(out)
		m.Status = old.Status
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if e = waitResponse(ctx, r.client, "blockstore", out); e != nil {
			apiError(&resp.Diagnostics, e)
			return
		}
	}
	v, err := readVolume(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Volume deletion protection enabled", "Set deletion_protection to false and apply before destroying this volume.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, "30m", &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "volumes", m.ID.ValueString())
	out, err := r.client.Do(ctx, "blockstore", "DELETE", p, nil)
	if deleteInProgress(err) {
		err = waitDeleting(ctx, r.client, "blockstore", p, err)
	}
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if !missing {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	if err = waitResponse(ctx, r.client, "blockstore", out); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	_, err = readVolume(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Volume deletion incomplete", "The operation succeeded but the volume is still returned by its API. Refresh and retry.")
		return
	}
	missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
	if !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client)
}
func (m *volumeModel) fromAPI(ctx context.Context, v volumeView, org string) {
	m.ID = types.StringValue(v.Slug)
	m.OrganizationID = types.StringValue(org)
	m.Name = types.StringValue(v.Name)
	m.VolumeType = types.StringValue(v.VolumeType)
	m.SizeBytes = types.StringValue(v.SizeBytes)
	m.IOPS = types.Int64Value(v.IOPS)
	m.ThroughputMBps = types.Int64Value(v.ThroughputMBps)
	m.DeletionProtection = types.BoolValue(v.DeletionProtection)
	m.Tags = mapValue(ctx, v.Tags)
	m.Status = types.StringValue(v.Status)
	if m.OperationID.IsUnknown() || m.OperationID.IsNull() {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue("30m")
	}
}
func readVolume(ctx context.Context, c *client.Client, p, id string) (volumeView, error) {
	var v volumeView
	out, err := c.Do(ctx, "blockstore", "GET", client.ProjectPath(c.OrganizationID, p, "volumes", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && (v.Slug == "" || v.SizeBytes == "") {
		err = fmt.Errorf("volume response omitted required identity or size")
	}
	return v, err
}
func validateGrowth(old, desired string) error {
	if err := validateSizeBytes(desired); err != nil {
		return err
	}
	a, ok := new(big.Int).SetString(old, 10)
	if !ok {
		return fmt.Errorf("stored volume size is invalid")
	}
	b, _ := new(big.Int).SetString(desired, 10)
	if b.Cmp(a) < 0 {
		return fmt.Errorf("volumes cannot shrink; increase size_bytes or create a separate volume")
	}
	return nil
}
func operationID(out *client.Response) types.String {
	var v struct{ ID string }
	if out.Decode(&v) != nil {
		return types.StringValue("")
	}
	return types.StringValue(v.ID)
}
func waitResponse(ctx context.Context, c *client.Client, service string, out *client.Response) error {
	if out.StatusCode != 202 {
		return nil
	}
	location := out.Header.Get("Location")
	if location == "" {
		return fmt.Errorf("accepted operation omitted Location; resource identity retained")
	}
	return c.Wait(ctx, service, location)
}
