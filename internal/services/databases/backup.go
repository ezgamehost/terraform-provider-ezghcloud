package databases

import (
	"context"
	"fmt"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type backupFields struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	DatabaseID     types.String `tfsdk:"database_id"`
	ResourceName   types.String `tfsdk:"resource_name"`
	Kind           types.String `tfsdk:"kind"`
	Engine         types.String `tfsdk:"engine"`
	EngineVersion  types.String `tfsdk:"engine_version"`
	SizeBytes      types.String `tfsdk:"size_bytes"`
	Status         types.String `tfsdk:"status"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
}
type backupModel struct {
	backupFields
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type backupView struct {
	Slug, ResourceName, Database, Kind, Engine, EngineVersion, SizeBytes, Status, CreatedAt string
	ExpiresAt                                                                               *string
}

type backupResource struct{ client *client.Client }

func (*backupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_backup"
}
func (*backupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: d, PlanModifiers: keepString}
	}
	resp.Schema = schema.Schema{Description: "A manual backup of a database. It outlives the database; destroying it deletes the backup.", Attributes: map[string]schema.Attribute{
		"id":                computed("Backup ID (dbb_)."),
		"organization_id":   computed("Organization managed by the configured provider."),
		"project_id":        schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the backup.", PlanModifiers: replace},
		"database_id":       schema.StringAttribute{Required: true, Description: "Database (dbi_) to back up. Changing it takes a new backup.", PlanModifiers: replace},
		"resource_name":     computed("IAM resource name."),
		"kind":              computed("manual."),
		"engine":            computed("Engine of the database it was taken of."),
		"engine_version":    computed("Engine version it was taken at."),
		"size_bytes":        computed("Size as an exact decimal byte count."),
		"status":            schema.StringAttribute{Computed: true, Description: "What the backup is doing."},
		"created_at":        computed("When it was taken."),
		"expires_at":        computed("When it expires; null for a manual backup kept until deleted."),
		"operation_id":      schema.StringAttribute{Computed: true, Description: "The operation that took the backup; unavailable on import.", PlanModifiers: keepString},
		"operation_timeout": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultTimeout), Description: "Maximum wait for the backup, for example 60m."},
	}}
}
func (r *backupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}
func (r *backupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m backupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if !strings.HasPrefix(m.DatabaseID.ValueString(), "dbi_") {
		resp.Diagnostics.AddAttributeError(path.Root("database_id"), "Invalid database ID", "Use the database's dbi_ ID.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "databases", m.DatabaseID.ValueString(), "backups"), nil)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.ID = types.StringValue(id)
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = waitResponse(ctx, r.client, out); err != nil {
		apiError(&resp.Diagnostics, fmt.Errorf("backup %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *backupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m backupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readBackup(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	if v.Status == "deleting" || v.Status == "deleted" {
		resp.State.RemoveResource(ctx)
		return
	}
	m.fill(v, r.client.OrganizationID)
	m.settle()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if v.Status != "creating" || m.OperationID.ValueString() == "" {
		return
	}
	waitCtx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	if err = r.client.Wait(waitCtx, service, client.OrgPath(r.client.OrganizationID, "operations", m.OperationID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.refresh(waitCtx, &m, &resp.State, &resp.Diagnostics)
}

// Update changes only operation_timeout: every other input replaces the backup.
func (r *backupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m backupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *backupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m backupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	// The API answers with the backup deleting, and it has left the project's lists.
	_, err := r.client.Do(ctx, service, "DELETE", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "backups", m.ID.ValueString()), nil)
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *backupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "dbb_")
}
func (r *backupResource) refresh(ctx context.Context, m *backupModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readBackup(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(diags, err)
		return
	}
	m.fill(v, r.client.OrganizationID)
	m.settle()
	diags.Append(state.Set(ctx, m)...)
}
func (m *backupModel) settle() {
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(defaultTimeout)
	}
}
func (f *backupFields) fill(v backupView, org string) {
	f.ID = types.StringValue(v.Slug)
	f.OrganizationID = types.StringValue(org)
	f.DatabaseID = types.StringValue(v.Database)
	f.ResourceName = types.StringValue(v.ResourceName)
	f.Kind = types.StringValue(v.Kind)
	f.Engine = types.StringValue(v.Engine)
	f.EngineVersion = types.StringValue(v.EngineVersion)
	f.SizeBytes = types.StringValue(v.SizeBytes)
	f.Status = types.StringValue(v.Status)
	f.CreatedAt = types.StringValue(v.CreatedAt)
	f.ExpiresAt = types.StringPointerValue(v.ExpiresAt)
}
func readBackup(ctx context.Context, c *client.Client, project, id string) (backupView, error) {
	var v backupView
	out, err := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, "backups", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = fmt.Errorf("backup response omitted its dbb_ identity")
	}
	return v, err
}
