package databases

import (
	"context"
	"errors"
	"fmt"

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// databaseFields are what the API shows; the data source reads exactly these.
type databaseFields struct {
	ID                    types.String `tfsdk:"id"`
	OrganizationID        types.String `tfsdk:"organization_id"`
	ProjectID             types.String `tfsdk:"project_id"`
	ResourceName          types.String `tfsdk:"resource_name"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	Tags                  types.Map    `tfsdk:"tags"`
	Engine                types.String `tfsdk:"engine"`
	EngineVersion         types.String `tfsdk:"engine_version"`
	InstanceType          types.String `tfsdk:"instance_type"`
	StorageVolumeType     types.String `tfsdk:"storage_volume_type"`
	StorageSizeGiB        types.Int64  `tfsdk:"storage_size_gib"`
	StorageIOPS           types.Int64  `tfsdk:"storage_iops"`
	StorageThroughputMBps types.Int64  `tfsdk:"storage_throughput_mbps"`
	VPCID                 types.String `tfsdk:"vpc_id"`
	SubnetID              types.String `tfsdk:"subnet_id"`
	AllowedSourceCIDRs    types.List   `tfsdk:"allowed_source_cidrs"`
	EndpointHostname      types.String `tfsdk:"endpoint_hostname"`
	EndpointPort          types.Int64  `tfsdk:"endpoint_port"`
	AdminSecretID         types.String `tfsdk:"admin_secret_id"`
	BackupRetentionDays   types.Int64  `tfsdk:"backup_retention_days"`
	BackupWindowUTC       types.String `tfsdk:"backup_window_utc"`
	MaintenanceWindowUTC  types.String `tfsdk:"maintenance_window_utc"`
	Parameters            types.Map    `tfsdk:"parameters"`
	LogExports            types.Set    `tfsdk:"log_exports"`
	DeletionProtection    types.Bool   `tfsdk:"deletion_protection"`
	Status                types.String `tfsdk:"status"`
}
type databaseModel struct {
	databaseFields
	SourceBackupID       types.String `tfsdk:"source_backup_id"`
	SourceDatabaseID     types.String `tfsdk:"source_database_id"`
	SourceTime           types.String `tfsdk:"source_time"`
	OperationID          types.String `tfsdk:"operation_id"`
	OperationTimeout     types.String `tfsdk:"operation_timeout"`
	FinalBackupOnDestroy types.Bool   `tfsdk:"final_backup_on_destroy"`
}
type databaseView struct {
	Slug, ResourceName, Name, Engine, EngineVersion, InstanceType, VPC, Subnet, AdminSecret, Status string
	Description                                                                                     *string
	Tags, Parameters                                                                                map[string]string
	Storage                                                                                         struct {
		VolumeType                    string
		SizeGiB, IOPS, ThroughputMBps int64
	}
	AllowedSourceCidrs, LogExports []string
	Endpoint                       *struct {
		Hostname string
		Port     int64
	}
	Backup struct {
		RetentionDays int64
		WindowUtc     string
	}
	Maintenance        struct{ WindowUtc string }
	DeletionProtection bool
}

type databaseResource struct{ client *client.Client }

const (
	noReplace = "a replacement would destroy the database's data, so a change is refused at plan."
	moveData  = "To move it, create a new database and move the data, or restore a backup or a point in time into a new database, then remove this one."
)

func (*databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

var (
	keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt    = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
)

func (*databaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: d, PlanModifiers: keepString}
	}
	optional := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Description: d, PlanModifiers: keepString}
	}
	optionalInt := func(d string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Description: d, PlanModifiers: keepInt}
	}
	source := func(d string) schema.StringAttribute {
		// Set once: a later change or removal must never replace (destroy) a restored database.
		return schema.StringAttribute{Optional: true, Description: d + " Only applies at create: a later change or removal is ignored."}
	}
	resp.Schema = schema.Schema{Description: "A managed PostgreSQL, Percona Server or MariaDB database, reached at one endpoint. Destroying it takes a final backup unless final_backup_on_destroy is false.", Attributes: map[string]schema.Attribute{
		"id":                      computed("Database ID (dbi_)."),
		"organization_id":         computed("Organization managed by the configured provider."),
		"project_id":              schema.StringAttribute{Required: true, Description: "Project identifier. It can't change: " + noReplace},
		"resource_name":           computed("IAM resource name."),
		"name":                    schema.StringAttribute{Required: true, Description: "Database name."},
		"description":             schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."},
		"tags":                    schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"engine":                  schema.StringAttribute{Optional: true, Computed: true, Description: "postgresql, percona or mariadb. Required unless restoring, where the source fixes it. It can't change: " + noReplace, PlanModifiers: keepString},
		"engine_version":          optional("Engine major version. Required unless restoring, where the source fixes it. It can't change here, because a replacement would destroy data: major upgrades go through a blue/green deployment, after which set it to the new version."),
		"instance_type":           schema.StringAttribute{Required: true, Description: "A gp-1 or gs-1 instance type. Changes in place. After a blue/green switchover to a new instance type, set it to that type."},
		"storage_volume_type":     optional("Storage volume type, gp1. It can't change: " + noReplace),
		"storage_size_gib":        schema.Int64Attribute{Required: true, Description: "Storage size in GiB. Grows in place; shrinking is refused."},
		"storage_iops":            optionalInt("Provisioned storage IOPS. Changes in place."),
		"storage_throughput_mbps": optionalInt("Provisioned storage throughput in MB/s. Changes in place."),
		"vpc_id":                  computed("VPC of the subnet."),
		"subnet_id":               schema.StringAttribute{Required: true, Description: "Subnet (sbn_) the endpoint lives in. It can't change: " + noReplace},
		"allowed_source_cidrs":    schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "1 to 20 IPv4 CIDRs allowed to connect. Left out, the VPC's ranges.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"endpoint_hostname":       computed("The endpoint's DNS name; null until it resolves."),
		"endpoint_port":           schema.Int64Attribute{Computed: true, Description: "The endpoint's port.", PlanModifiers: keepInt},
		"admin_secret_id":         computed("The Secret (sec_) holding the admin username and password. Terraform state never holds the password."),
		"backup_retention_days":   optionalInt("Days automated backups are kept, 1 to 35."),
		"backup_window_utc":       optional("Daily backup window, HH:MM-HH:MM UTC, at least 30 minutes."),
		"maintenance_window_utc":  optional("Weekly maintenance window, ddd:HH:MM-ddd:HH:MM UTC, at least 30 minutes."),
		"parameters":              schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Engine parameters. The whole set: a key left out goes back to its default. Only configured keys are read back."},
		"log_exports":             schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), Description: "Engine logs sent to Observability: error, slow and audit."},
		"deletion_protection":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "While true, the database can't be destroyed."},
		"status":                  schema.StringAttribute{Computed: true, Description: "What the database is doing."},
		"source_backup_id":        source("Restore this backup (dbb_) into the new database. Set it alone."),
		"source_database_id":      source("Restore this database (dbi_) at source_time into the new database."),
		"source_time":             source("Point in time to restore source_database_id at, an RFC 3339 instant."),
		"operation_id":            schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultTimeout), Description: "Maximum wait for each asynchronous operation, for example 60m."},
		"final_backup_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Take a final backup, which outlives the database, when destroying it."},
	}}
}
func (r *databaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *databaseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.State.Raw.IsNull() {
		if plan.SourceBackupID.IsUnknown() || plan.SourceDatabaseID.IsUnknown() || plan.SourceTime.IsUnknown() {
			return // checked again once known, at apply
		}
		b, d, t := plan.SourceBackupID.ValueString() != "", plan.SourceDatabaseID.ValueString() != "", plan.SourceTime.ValueString() != ""
		if (b && (d || t)) || d != t {
			resp.Diagnostics.AddError("Invalid restore source", "Set source_backup_id alone, or source_database_id with source_time.")
		}
		return
	}
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Nothing replaces a database. An unknown value counts as a change: a replaced subnet or project.
	for name, pair := range map[string][2]types.String{"project_id": {plan.ProjectID, state.ProjectID}, "engine": {plan.Engine, state.Engine}, "subnet_id": {plan.SubnetID, state.SubnetID}, "storage_volume_type": {plan.StorageVolumeType, state.StorageVolumeType}} {
		if !pair[0].Equal(pair[1]) {
			to := "a value known only at apply"
			if known(pair[0]) {
				to = pair[0].ValueString()
			}
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" can't change", fmt.Sprintf("Changing %s from %s to %s would replace the database and destroy its data and its point-in-time history. %s", name, pair[1].ValueString(), to, moveData))
		}
	}
	// The database is never replaced, so a source change only reaches state.
	for name, pair := range map[string][2]types.String{"source_backup_id": {plan.SourceBackupID, state.SourceBackupID}, "source_database_id": {plan.SourceDatabaseID, state.SourceDatabaseID}, "source_time": {plan.SourceTime, state.SourceTime}} {
		if !pair[0].Equal(pair[1]) {
			resp.Diagnostics.AddAttributeWarning(path.Root(name), name+" only applies at create",
				"A restore source is used only when the database is created. This change is saved to state and does nothing to the database; to restore again, create a new database.")
		}
	}
	if known(plan.EngineVersion) && !plan.EngineVersion.Equal(state.EngineVersion) {
		resp.Diagnostics.AddAttributeError(path.Root("engine_version"), "engine_version can't change",
			fmt.Sprintf("Changing engine_version from %s to %s would replace the database and destroy its data. Major upgrades go through a blue/green deployment; after its switchover, set engine_version to the new version.", state.EngineVersion.ValueString(), plan.EngineVersion.ValueString()))
	}
	if known(plan.InstanceType) && !plan.InstanceType.Equal(state.InstanceType) {
		resp.Diagnostics.AddAttributeWarning(path.Root("instance_type"), "instance_type resizes the database",
			fmt.Sprintf("Applying resizes the database from %[1]s to %[2]s. If a blue/green deployment's switchover moved it to %[1]s, set instance_type to %[1]s instead.", state.InstanceType.ValueString(), plan.InstanceType.ValueString()))
	}
	if known(plan.StorageSizeGiB) && plan.StorageSizeGiB.ValueInt64() < state.StorageSizeGiB.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("storage_size_gib"), "Storage can't shrink", fmt.Sprintf("storage_size_gib must be at least %d.", state.StorageSizeGiB.ValueInt64()))
	}
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "tags": tags, "instanceType": m.InstanceType.ValueString(), "subnet": m.SubnetID.ValueString(), "deletionProtection": m.DeletionProtection.ValueBool()}
	setString(body, "description", m.Description)
	setString(body, "engine", m.Engine)
	setString(body, "engineVersion", m.EngineVersion)
	storage := map[string]any{"sizeGiB": m.StorageSizeGiB.ValueInt64()}
	setString(storage, "volumeType", m.StorageVolumeType)
	setInt(storage, "iops", m.StorageIOPS)
	setInt(storage, "throughputMBps", m.StorageThroughputMBps)
	body["storage"] = storage
	backup := map[string]any{}
	setInt(backup, "retentionDays", m.BackupRetentionDays)
	setString(backup, "windowUtc", m.BackupWindowUTC)
	body["backup"] = backup
	maintenance := map[string]any{}
	setString(maintenance, "windowUtc", m.MaintenanceWindowUTC)
	body["maintenance"] = maintenance
	resp.Diagnostics.Append(setCollections(ctx, body, m, true)...)
	if m.SourceBackupID.ValueString() != "" {
		body["source"] = map[string]any{"backup": m.SourceBackupID.ValueString()}
	} else if m.SourceDatabaseID.ValueString() != "" {
		body["source"] = map[string]any{"database": m.SourceDatabaseID.ValueString(), "time": m.SourceTime.ValueString()}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "databases"), body)
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
	m.Status = types.StringValue("creating")
	m.OperationID = operationID(out)
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = waitResponse(ctx, r.client, out); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// An error would taint it, and the next apply would destroy a database still being created.
			resp.Diagnostics.AddWarning("Database still being created", fmt.Sprintf("Database %s was still being created when the wait ended. The next refresh or apply waits for operation %s.", id, m.OperationID.ValueString()))
			return
		}
		apiError(&resp.Diagnostics, fmt.Errorf("database %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readDatabase(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
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
	// A deleting database stays in state until it's gone: Delete saves no operation to wait on.
	if v.Status != "creating" {
		return
	}
	if m.OperationID.ValueString() == "" {
		resp.Diagnostics.AddError("Database operation in progress", "The database is "+v.Status+" and its operation ID is unavailable. Retry refresh after the operation completes.")
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

func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Name, tags and protection are row changes; the rest only goes when it changed, since each reaches the database.
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags, "deletionProtection": m.DeletionProtection.ValueBool()}
	if !m.InstanceType.Equal(old.InstanceType) {
		body["instanceType"] = m.InstanceType.ValueString()
	}
	if !m.StorageSizeGiB.Equal(old.StorageSizeGiB) || !m.StorageIOPS.Equal(old.StorageIOPS) || !m.StorageThroughputMBps.Equal(old.StorageThroughputMBps) {
		storage := map[string]any{"sizeGiB": m.StorageSizeGiB.ValueInt64()}
		setInt(storage, "iops", m.StorageIOPS)
		setInt(storage, "throughputMBps", m.StorageThroughputMBps)
		body["storage"] = storage
	}
	if !m.BackupRetentionDays.Equal(old.BackupRetentionDays) || !m.BackupWindowUTC.Equal(old.BackupWindowUTC) {
		backup := map[string]any{}
		setInt(backup, "retentionDays", m.BackupRetentionDays)
		setString(backup, "windowUtc", m.BackupWindowUTC)
		body["backup"] = backup
	}
	if !m.MaintenanceWindowUTC.Equal(old.MaintenanceWindowUTC) {
		maintenance := map[string]any{}
		setString(maintenance, "windowUtc", m.MaintenanceWindowUTC)
		body["maintenance"] = maintenance
	}
	changed := databaseModel{}
	changed.AllowedSourceCIDRs = types.ListNull(types.StringType)
	changed.LogExports = types.SetNull(types.StringType)
	changed.Parameters = types.MapNull(types.StringType)
	if !m.AllowedSourceCIDRs.Equal(old.AllowedSourceCIDRs) {
		changed.AllowedSourceCIDRs = m.AllowedSourceCIDRs
	}
	if !m.LogExports.Equal(old.LogExports) {
		changed.LogExports = m.LogExports
	}
	resp.Diagnostics.Append(setCollections(ctx, body, changed, false)...)
	if !m.Parameters.Equal(old.Parameters) {
		// The API takes the whole set: keep keys Terraform never managed, drop the ones removed from config.
		current, err := readDatabase(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		params := map[string]string{}
		for k, v := range current.Parameters {
			params[k] = v
		}
		for k := range old.Parameters.Elements() {
			delete(params, k)
		}
		if known(m.Parameters) {
			planned := map[string]string{}
			resp.Diagnostics.Append(m.Parameters.ElementsAs(ctx, &planned, false)...)
			for k, v := range planned {
				params[k] = v
			}
		}
		body["parameters"] = params
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if len(body) == 4 && m.Name.Equal(old.Name) && m.Description.Equal(old.Description) && m.Tags.Equal(old.Tags) && m.DeletionProtection.Equal(old.DeletionProtection) {
		m.OperationID = old.OperationID // only provider settings changed
		r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "databases", m.ID.ValueString()), body)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.OperationID = operationID(out)
	m.Status = old.Status
	resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
	if err = waitResponse(ctx, r.client, out); err != nil {
		apiError(&resp.Diagnostics, fmt.Errorf("database %s, operation %s: %w", m.ID.ValueString(), m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Database deletion protection enabled", "Set deletion_protection to false and apply before destroying this database.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	final := "true"
	if !m.FinalBackupOnDestroy.IsNull() && !m.FinalBackupOnDestroy.ValueBool() {
		final = "false"
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "databases", m.ID.ValueString())
	out, err := r.client.Do(ctx, service, "DELETE", p+"?finalBackup="+final, nil)
	if deleteInProgress(err) {
		err = waitDeleting(ctx, r.client, service, p, err)
	}
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if !missing {
			apiError(&resp.Diagnostics, e)
		}
		return
	}
	if err = waitResponse(ctx, r.client, out); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	_, err = readDatabase(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Database deletion incomplete", "The operation succeeded but the database is still returned by its API. Refresh and retry.")
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "dbi_")
}

// refresh reads the database into m and state after an operation.
func (r *databaseResource) refresh(ctx context.Context, m *databaseModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readDatabase(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(diags, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	diags.Append(state.Set(ctx, m)...)
}

func (f *databaseFields) fill(ctx context.Context, v databaseView, org string) {
	f.ID = types.StringValue(v.Slug)
	f.OrganizationID = types.StringValue(org)
	f.ResourceName = types.StringValue(v.ResourceName)
	f.Name = types.StringValue(v.Name)
	f.Description = types.StringPointerValue(v.Description)
	f.Tags = mapValue(ctx, v.Tags)
	f.Engine = types.StringValue(v.Engine)
	f.EngineVersion = types.StringValue(v.EngineVersion)
	f.InstanceType = types.StringValue(v.InstanceType)
	f.StorageVolumeType = types.StringValue(v.Storage.VolumeType)
	f.StorageSizeGiB = types.Int64Value(v.Storage.SizeGiB)
	f.StorageIOPS = types.Int64Value(v.Storage.IOPS)
	f.StorageThroughputMBps = types.Int64Value(v.Storage.ThroughputMBps)
	f.VPCID = types.StringValue(v.VPC)
	f.SubnetID = types.StringValue(v.Subnet)
	f.AllowedSourceCIDRs = stringList(ctx, v.AllowedSourceCidrs)
	f.EndpointHostname, f.EndpointPort = types.StringNull(), types.Int64Null()
	if v.Endpoint != nil {
		f.EndpointHostname, f.EndpointPort = types.StringValue(v.Endpoint.Hostname), types.Int64Value(v.Endpoint.Port)
	}
	f.AdminSecretID = types.StringValue(v.AdminSecret)
	f.BackupRetentionDays = types.Int64Value(v.Backup.RetentionDays)
	f.BackupWindowUTC = types.StringValue(v.Backup.WindowUtc)
	f.MaintenanceWindowUTC = types.StringValue(v.Maintenance.WindowUtc)
	f.Parameters = mapValue(ctx, v.Parameters)
	if v.LogExports == nil {
		v.LogExports = []string{}
	}
	f.LogExports, _ = types.SetValueFrom(ctx, types.StringType, v.LogExports)
	f.DeletionProtection = types.BoolValue(v.DeletionProtection)
	f.Status = types.StringValue(v.Status)
}

// fromAPI reads the database into the resource. parameters keeps the configured keys only, so a
// default the API adds isn't drift; an import reads none of them.
func (m *databaseModel) fromAPI(ctx context.Context, v databaseView, org string) {
	configured, description := m.Parameters, m.Description
	m.fill(ctx, v, org)
	if description.ValueString() == "" && known(description) && v.Description == nil {
		m.Description = description // the API shows an empty description as null
	}
	m.Parameters = types.MapNull(types.StringType)
	if known(configured) {
		keep := map[string]string{}
		for k := range configured.Elements() {
			if value, ok := v.Parameters[k]; ok {
				keep[k] = value
			}
		}
		m.Parameters = mapValue(ctx, keep)
	}
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(defaultTimeout)
	}
	if m.FinalBackupOnDestroy.IsNull() {
		m.FinalBackupOnDestroy = types.BoolValue(true)
	}
}

func readDatabase(ctx context.Context, c *client.Client, project, id string) (databaseView, error) {
	var v databaseView
	out, err := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, "databases", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = fmt.Errorf("database response omitted its dbi_ identity")
	}
	return v, err
}

// setCollections adds allowed_source_cidrs, log_exports and (on create) parameters when set.
func setCollections(ctx context.Context, body map[string]any, m databaseModel, create bool) diag.Diagnostics {
	var d diag.Diagnostics
	if known(m.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.LogExports) {
		logs := []string{}
		d.Append(m.LogExports.ElementsAs(ctx, &logs, false)...)
		body["logExports"] = logs
	}
	if create && known(m.Parameters) {
		params := map[string]string{}
		d.Append(m.Parameters.ElementsAs(ctx, &params, false)...)
		body["parameters"] = params
	}
	return d
}
func setString(body map[string]any, key string, v types.String) {
	if known(v) {
		body[key] = v.ValueString()
	}
}
func setInt(body map[string]any, key string, v types.Int64) {
	if known(v) {
		body[key] = v.ValueInt64()
	}
}
func stringList(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	out, _ := types.ListValueFrom(ctx, types.StringType, v)
	return out
}
