package warehouse

import (
	"context"
	"errors"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// warehouseFields are what the API shows; the data source reads exactly these.
type warehouseFields struct {
	ID                          types.String `tfsdk:"id"`
	OrganizationID              types.String `tfsdk:"organization_id"`
	ProjectID                   types.String `tfsdk:"project_id"`
	ResourceName                types.String `tfsdk:"resource_name"`
	Name                        types.String `tfsdk:"name"`
	Description                 types.String `tfsdk:"description"`
	Tags                        types.Map    `tfsdk:"tags"`
	EngineVersion               types.String `tfsdk:"engine_version"`
	Shards                      types.Int64  `tfsdk:"shards"`
	Replicas                    types.Int64  `tfsdk:"replicas"`
	Keeper                      types.String `tfsdk:"keeper"`
	InstanceType                types.String `tfsdk:"instance_type"`
	StorageKind                 types.String `tfsdk:"storage_kind"`
	StorageSizeGiB              types.Int64  `tfsdk:"storage_size_gib"`
	StorageIOPS                 types.Int64  `tfsdk:"storage_iops"`
	StorageThroughputMBps       types.Int64  `tfsdk:"storage_throughput_mbps"`
	VPCID                       types.String `tfsdk:"vpc_id"`
	SubnetID                    types.String `tfsdk:"subnet_id"`
	AllowedSourceCIDRs          types.List   `tfsdk:"allowed_source_cidrs"`
	PlaintextPort               types.Bool   `tfsdk:"plaintext_port"`
	BackupsEnabled              types.Bool   `tfsdk:"backups_enabled"`
	BackupRetentionDays         types.Int64  `tfsdk:"backup_retention_days"`
	BackupWindowUTC             types.String `tfsdk:"backup_window_utc"`
	MaintenanceWindowUTC        types.String `tfsdk:"maintenance_window_utc"`
	DeletionProtection          types.Bool   `tfsdk:"deletion_protection"`
	EndpointHostname            types.String `tfsdk:"endpoint_hostname"`
	EndpointHTTPSPort           types.Int64  `tfsdk:"endpoint_https_port"`
	EndpointNativePort          types.Int64  `tfsdk:"endpoint_native_port"`
	EndpointPlaintextHTTPPort   types.Int64  `tfsdk:"endpoint_plaintext_http_port"`
	EndpointPlaintextNativePort types.Int64  `tfsdk:"endpoint_plaintext_native_port"`
	Status                      types.String `tfsdk:"status"`
}
type warehouseModel struct {
	warehouseFields
	FromBackup       types.String `tfsdk:"from_backup"`
	AppSecretID      types.String `tfsdk:"app_secret_id"`
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type warehouseView struct {
	Slug, ResourceName, Name, EngineVersion, InstanceType, VPC, Subnet, Status string
	Description                                                                *string
	Keeper                                                                     *string
	Tags                                                                       map[string]string
	Shards, Replicas                                                           int64
	Storage                                                                    struct {
		Kind                 string
		SizeGiB              int64
		IOPS, ThroughputMBps *int64
	}
	AllowedSourceCidrs                []string
	PlaintextPort, DeletionProtection bool
	Endpoint                          *struct {
		Hostname            string
		HTTPSPort           int64 `json:"httpsPort"`
		NativePort          int64
		PlaintextHTTPPort   *int64 `json:"plaintextHttpPort"`
		PlaintextNativePort *int64
	}
	Backups struct {
		Enabled       bool
		RetentionDays int64
		WindowUtc     string
	}
	Maintenance struct{ WindowUtc string }
}

var (
	keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt    = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	replace    = []planmodifier.String{stringplanmodifier.RequiresReplace()}
)

type warehouseResource struct{ client *client.Client }

func (*warehouseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_warehouse"
}

func (*warehouseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: d, PlanModifiers: keepString}
	}
	computedInt := func(d string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: d, PlanModifiers: keepInt}
	}
	optionalInt := func(d string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Description: d, PlanModifiers: keepInt}
	}
	resp.Schema = schema.Schema{Description: "A managed ClickHouse warehouse in a subnet of your VPC: 1 to 8 shards, each with 1 to 3 replicas. Clients connect over HTTPS and the native protocol with TLS, with a username and password. It comes with the database default and the user app, whose password is in a Secret (app_secret_id). This resource never creates, changes or deletes a database or a user: use ezghcloud_warehouse_database and ezghcloud_warehouse_user. Destroying it deletes the warehouse, every database and table in it, its backups and its users' Secrets.", Attributes: map[string]schema.Attribute{
		"id":              computed("Warehouse ID (whs_)."),
		"organization_id": computed("Organization managed by the configured provider."),
		"project_id":      schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the warehouse.", PlanModifiers: replace},
		"resource_name":   computed("IAM resource name."),
		"name":            schema.StringAttribute{Required: true, Description: "Warehouse name. Changes in place."},
		"description":     schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."},
		"tags":            schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"engine_version":  schema.StringAttribute{Required: true, Description: "ClickHouse LTS version, 26.3 or 26.8. A newer version upgrades in place, one node at a time (one-way); an older one is refused at plan."},
		"shards":          schema.Int64Attribute{Optional: true, Computed: true, Description: "Shards, 1 to 8 (1 when left out). More shards are added in place: existing data stays on the current shards, and rows inserted through a Distributed table spread across all shards by its sharding key. Fewer are refused at plan.", PlanModifiers: keepInt},
		"replicas":        schema.Int64Attribute{Optional: true, Computed: true, Description: "Replicas of each shard, 1 to 3 (1 when left out). Each shard keeps serving through the loss of one node with 2 or 3 replicas. More are added in place; fewer are refused at plan.", PlanModifiers: keepInt},
		"keeper":          schema.StringAttribute{Optional: true, Computed: true, Description: "Where Keeper runs: dedicated or colocated (3 or more nodes of at least 8 vCPUs and 32 GiB). Null for 1 shard × 1 replica; dedicated when left out on anything larger. Changes in place.", PlanModifiers: keepString},
		"instance_type":   schema.StringAttribute{Required: true, Description: "Every node's instance type, with at least 8 GiB of memory. Changes in place, one replica of each shard at a time."},
		"storage_kind": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("volume"), Description: "volume. Changing it replaces the warehouse.", PlanModifiers: replace,
			Validators: []validator.String{nameValidator{"storage kind", func(s string) string {
				if s != "volume" {
					return "storage_kind is volume."
				}
				return ""
			}}}},
		"storage_size_gib":               schema.Int64Attribute{Required: true, Description: "Each node's storage in GiB, at least 10. Grows in place; shrinking is refused at plan."},
		"storage_iops":                   optionalInt("Each node's provisioned storage IOPS. Set at create."),
		"storage_throughput_mbps":        optionalInt("Each node's provisioned storage throughput in MB/s. Set at create."),
		"vpc_id":                         computed("VPC of the subnet."),
		"subnet_id":                      schema.StringAttribute{Required: true, Description: "Subnet (sbn_) the warehouse is in. Changing it replaces the warehouse.", PlanModifiers: replace},
		"allowed_source_cidrs":           schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "1 to 20 IPv4 CIDRs allowed to connect. Left out at create, the VPC's ranges; removing it later keeps the current list.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"plaintext_port":                 schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Also serve HTTP on 8123 and the native protocol on 9000 without TLS, inside the VPC. Changes in place."},
		"backups_enabled":                schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Daily backups of every database. Changes in place."},
		"backup_retention_days":          optionalInt("How long backups are kept, 1 to 35 days (7 when left out). Changes in place."),
		"backup_window_utc":              schema.StringAttribute{Optional: true, Computed: true, Description: "When the daily backup is taken, HH:MM-HH:MM UTC. Changes in place.", PlanModifiers: keepString},
		"maintenance_window_utc":         schema.StringAttribute{Optional: true, Computed: true, Description: "Weekly maintenance window, ddd:HH:MM-ddd:HH:MM UTC. Changes in place.", PlanModifiers: keepString},
		"deletion_protection":            schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "While true, neither the warehouse nor its databases can be destroyed."},
		"from_backup":                    schema.StringAttribute{Optional: true, Description: "Restore this backup into the new warehouse, <warehouse ID>/<backup ID> (a backup's ID is a UUID); shards must equal the backup's, and it needs warehouse.warehouses.query on the backup's warehouse. Its users come across with new passwords in new Secrets (import them to manage them); connections don't. Changing it replaces the warehouse.", PlanModifiers: replace},
		"endpoint_hostname":              computed("The name clients connect to; null until it resolves. It stays the same through scaling and upgrades."),
		"endpoint_https_port":            computedInt("The HTTPS port, 8443."),
		"endpoint_native_port":           computedInt("The native protocol's TLS port, 9440."),
		"endpoint_plaintext_http_port":   schema.Int64Attribute{Computed: true, Description: "8123 while plaintext_port is true; null otherwise."},
		"endpoint_plaintext_native_port": schema.Int64Attribute{Computed: true, Description: "9000 while plaintext_port is true; null otherwise."},
		"app_secret_id":                  computed("The Secret (sec_) holding the app user's username, password and how to connect."),
		"status":                         schema.StringAttribute{Computed: true, Description: "What the warehouse is doing."},
		"operation_id":                   schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout":              schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultTimeout), Description: "Maximum wait for each asynchronous operation, for example 60m."},
	}}
}
func (r *warehouseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// compareVersions orders "26.3" < "26.8" < "26.10", each dotted part as a number.
func compareVersions(a, b string) int {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(x), len(y)) {
		var p, q int
		if i < len(x) {
			p, _ = strconv.Atoi(x[i])
		}
		if i < len(y) {
			q, _ = strconv.Atoi(y[i])
		}
		if p != q {
			return p - q
		}
	}
	return 0
}

// ModifyPlan refuses what only grows going down, and marks what a change moves as unknown.
func (r *warehouseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state warehouseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	// A replacement makes a new warehouse: any shape, version or size is fine then.
	if resp.Diagnostics.HasError() || !plan.ProjectID.Equal(state.ProjectID) || !plan.SubnetID.Equal(state.SubnetID) || !plan.StorageKind.Equal(state.StorageKind) || !plan.FromBackup.Equal(state.FromBackup) {
		return
	}
	down := func(name string, p, s types.Int64, what string) {
		if known(p) && known(s) && p.ValueInt64() < s.ValueInt64() {
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" can't go down", fmt.Sprintf("%s must be at least %d.", what, s.ValueInt64()))
		}
	}
	down("shards", plan.Shards, state.Shards, "shards")
	down("replicas", plan.Replicas, state.Replicas, "replicas")
	down("storage_size_gib", plan.StorageSizeGiB, state.StorageSizeGiB, "storage_size_gib")
	if known(plan.EngineVersion) && compareVersions(plan.EngineVersion.ValueString(), state.EngineVersion.ValueString()) < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("engine_version"), "engine_version can't go back",
			fmt.Sprintf("Upgrades are one-way: %s can't move to the older %s.", state.EngineVersion.ValueString(), plan.EngineVersion.ValueString()))
	}
	for name, v := range map[string][2]types.Int64{"storage_iops": {plan.StorageIOPS, state.StorageIOPS}, "storage_throughput_mbps": {plan.StorageThroughputMBps, state.StorageThroughputMBps}} {
		if known(v[0]) && known(v[1]) && !v[0].Equal(v[1]) {
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" is set at create", name+" can't change on an existing warehouse.")
		}
	}
	// Growing from one node picks a Keeper mode when none is configured.
	var keeper types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("keeper"), &keeper)...)
	if keeper.IsNull() && (!plan.Shards.Equal(state.Shards) || !plan.Replicas.Equal(state.Replicas)) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("keeper"), types.StringUnknown())...)
	}
	// A name still resolving keeps nothing from state: it comes with the next read.
	if state.EndpointHostname.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint_hostname"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint_https_port"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint_native_port"), types.Int64Unknown())...)
	}
	if state.AppSecretID.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("app_secret_id"), types.StringUnknown())...)
	}
	if !plan.PlaintextPort.Equal(state.PlaintextPort) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint_plaintext_http_port"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint_plaintext_native_port"), types.Int64Unknown())...)
	}
}

func (r *warehouseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m warehouseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	body, d := createBody(ctx, m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "warehouses"), body)
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
			resp.Diagnostics.AddWarning("Warehouse still being created", fmt.Sprintf("Warehouse %s was still being created when the wait ended. The next refresh or apply waits for operation %s.", id, m.OperationID.ValueString()))
			return
		}
		apiError(&resp.Diagnostics, fmt.Errorf("warehouse %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

// createBody is POST /warehouses': what's unset is left to the API.
func createBody(ctx context.Context, m warehouseModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	tags, td := stringMap(ctx, m.Tags)
	d.Append(td...)
	storage := map[string]any{"kind": m.StorageKind.ValueString(), "sizeGiB": m.StorageSizeGiB.ValueInt64()}
	setInt(storage, "iops", m.StorageIOPS)
	setInt(storage, "throughputMBps", m.StorageThroughputMBps)
	body := map[string]any{"name": m.Name.ValueString(), "tags": tags, "engineVersion": m.EngineVersion.ValueString(), "instanceType": m.InstanceType.ValueString(),
		"storage": storage, "subnet": m.SubnetID.ValueString(), "plaintextPort": m.PlaintextPort.ValueBool(), "deletionProtection": m.DeletionProtection.ValueBool()}
	setString(body, "description", m.Description)
	setInt(body, "shards", m.Shards)
	setInt(body, "replicas", m.Replicas)
	setString(body, "keeper", m.Keeper)
	backups := map[string]any{"enabled": m.BackupsEnabled.ValueBool()}
	setInt(backups, "retentionDays", m.BackupRetentionDays)
	setString(backups, "windowUtc", m.BackupWindowUTC)
	body["backups"] = backups
	if known(m.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	if known(m.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.FromBackup) {
		wh, backup, ok := strings.Cut(m.FromBackup.ValueString(), "/")
		if !ok || wh == "" || backup == "" {
			d.AddAttributeError(path.Root("from_backup"), "Invalid from_backup", "Use <warehouse ID>/<backup ID>.")
		}
		body["fromBackup"] = map[string]any{"warehouse": wh, "backup": backup}
	}
	return body, d
}

// updateBody is the PATCH for everything but the shape, the Keeper mode and an upgrade, which the
// API takes on their own: only what changed.
func updateBody(ctx context.Context, m, old warehouseModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	body := map[string]any{}
	if !m.Name.Equal(old.Name) {
		body["name"] = m.Name.ValueString()
	}
	if !m.Description.Equal(old.Description) {
		body["description"] = m.Description.ValueStringPointer()
	}
	if !m.Tags.Equal(old.Tags) {
		tags, td := stringMap(ctx, m.Tags)
		d.Append(td...)
		body["tags"] = tags
	}
	if !m.DeletionProtection.Equal(old.DeletionProtection) {
		body["deletionProtection"] = m.DeletionProtection.ValueBool()
	}
	if !m.InstanceType.Equal(old.InstanceType) {
		body["instanceType"] = m.InstanceType.ValueString()
	}
	if !m.StorageSizeGiB.Equal(old.StorageSizeGiB) {
		body["storage"] = map[string]any{"sizeGiB": m.StorageSizeGiB.ValueInt64()}
	}
	if known(m.AllowedSourceCIDRs) && !m.AllowedSourceCIDRs.Equal(old.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.PlaintextPort) && !m.PlaintextPort.Equal(old.PlaintextPort) {
		body["plaintextPort"] = m.PlaintextPort.ValueBool()
	}
	backups := map[string]any{}
	if known(m.BackupsEnabled) && !m.BackupsEnabled.Equal(old.BackupsEnabled) {
		backups["enabled"] = m.BackupsEnabled.ValueBool()
	}
	if known(m.BackupRetentionDays) && !m.BackupRetentionDays.Equal(old.BackupRetentionDays) {
		backups["retentionDays"] = m.BackupRetentionDays.ValueInt64()
	}
	if known(m.BackupWindowUTC) && !m.BackupWindowUTC.Equal(old.BackupWindowUTC) {
		backups["windowUtc"] = m.BackupWindowUTC.ValueString()
	}
	if len(backups) > 0 {
		body["backups"] = backups
	}
	if known(m.MaintenanceWindowUTC) && !m.MaintenanceWindowUTC.Equal(old.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	return body, d
}

func (r *warehouseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m warehouseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readWarehouse(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
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
	r.appSecret(ctx, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if v.Status != "creating" {
		return
	}
	if m.OperationID.ValueString() == "" {
		resp.Diagnostics.AddError("Warehouse operation in progress", "The warehouse is "+v.Status+" and its operation ID is unavailable. Retry refresh after the operation completes.")
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

// Update sends the settings that changed, then the shape, then the Keeper mode, then an upgrade, each
// on its own: the API takes them apart from the rest. Growing from one node carries its Keeper mode
// with the shape.
func (r *warehouseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old warehouseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	body, d := updateBody(ctx, m, old)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "warehouses", m.ID.ValueString())
	m.OperationID, m.Status = old.OperationID, old.Status
	patch := func(b map[string]any) bool {
		out, err := r.client.Do(ctx, service, "PATCH", p, b)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return false
		}
		m.OperationID = operationID(out)
		if err = waitResponse(ctx, r.client, out); err != nil {
			apiError(&resp.Diagnostics, fmt.Errorf("warehouse %s, operation %s: %w", m.ID.ValueString(), m.OperationID.ValueString(), err))
			return false
		}
		return true
	}
	if len(body) > 0 && !patch(body) {
		return
	}
	keeperChanged := known(m.Keeper) && !m.Keeper.Equal(old.Keeper)
	shape := map[string]any{}
	if known(m.Shards) && !m.Shards.Equal(old.Shards) {
		shape["shards"] = m.Shards.ValueInt64()
	}
	if known(m.Replicas) && !m.Replicas.Equal(old.Replicas) {
		shape["replicas"] = m.Replicas.ValueInt64()
	}
	if len(shape) > 0 {
		if old.Keeper.IsNull() && keeperChanged {
			shape["keeper"] = m.Keeper.ValueString()
			keeperChanged = false
		}
		if !patch(shape) {
			return
		}
	}
	if keeperChanged && !patch(map[string]any{"keeper": m.Keeper.ValueString()}) {
		return
	}
	if !m.EngineVersion.Equal(old.EngineVersion) && !patch(map[string]any{"engineVersion": m.EngineVersion.ValueString()}) {
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *warehouseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m warehouseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Warehouse deletion protection enabled", "Set deletion_protection to false and apply before destroying this warehouse.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "warehouses", m.ID.ValueString())
	out, err := r.client.Do(ctx, service, "DELETE", p, nil)
	if deleteInProgress(err) {
		err = waitDeleting(ctx, r.client, p, err)
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
	_, err = readWarehouse(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Warehouse deletion incomplete", "The operation succeeded but the warehouse is still returned by its API. Refresh and retry.")
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}

// ImportState takes organization_id/project_id/whs_….
func (r *warehouseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "whs_") || !prefixedID.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/whs_<12-character ID>.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "id"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
}

// refresh reads the warehouse and its app user's Secret (a GET of the user; nothing else of the
// warehouse's is read).
func (r *warehouseResource) refresh(ctx context.Context, m *warehouseModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readWarehouse(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(diags, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	r.appSecret(ctx, m)
	diags.Append(state.Set(ctx, m)...)
}

// appSecret reads the app user's Secret ID while it isn't known (after a create, an import or a
// wait that ended early). It stays null when the user can't be read.
func (r *warehouseResource) appSecret(ctx context.Context, m *warehouseModel) {
	if known(m.AppSecretID) {
		return
	}
	m.AppSecretID = types.StringNull()
	out, err := r.client.Do(ctx, service, "GET", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "warehouses", m.ID.ValueString(), "users", "app"), nil)
	var u struct{ Secret *string }
	if err == nil && out.Decode(&u) == nil {
		m.AppSecretID = types.StringPointerValue(u.Secret)
	}
}

func (f *warehouseFields) fill(ctx context.Context, v warehouseView, org string) {
	f.ID = types.StringValue(v.Slug)
	f.OrganizationID = types.StringValue(org)
	f.ResourceName = types.StringValue(v.ResourceName)
	f.Name = types.StringValue(v.Name)
	f.Description = types.StringPointerValue(v.Description)
	f.Tags = mapValue(ctx, v.Tags)
	f.EngineVersion = types.StringValue(v.EngineVersion)
	f.Shards, f.Replicas = types.Int64Value(v.Shards), types.Int64Value(v.Replicas)
	f.Keeper = types.StringPointerValue(v.Keeper)
	f.InstanceType = types.StringValue(v.InstanceType)
	f.StorageKind, f.StorageSizeGiB = types.StringValue(v.Storage.Kind), types.Int64Value(v.Storage.SizeGiB)
	f.StorageIOPS, f.StorageThroughputMBps = types.Int64PointerValue(v.Storage.IOPS), types.Int64PointerValue(v.Storage.ThroughputMBps)
	f.VPCID = types.StringValue(v.VPC)
	f.SubnetID = types.StringValue(v.Subnet)
	f.AllowedSourceCIDRs = stringList(ctx, v.AllowedSourceCidrs)
	f.PlaintextPort = types.BoolValue(v.PlaintextPort)
	f.BackupsEnabled, f.BackupRetentionDays, f.BackupWindowUTC = types.BoolValue(v.Backups.Enabled), types.Int64Value(v.Backups.RetentionDays), types.StringValue(v.Backups.WindowUtc)
	f.EndpointHostname, f.EndpointHTTPSPort, f.EndpointNativePort, f.EndpointPlaintextHTTPPort, f.EndpointPlaintextNativePort = types.StringNull(), types.Int64Null(), types.Int64Null(), types.Int64Null(), types.Int64Null()
	if e := v.Endpoint; e != nil {
		f.EndpointHostname, f.EndpointHTTPSPort, f.EndpointNativePort = types.StringValue(e.Hostname), types.Int64Value(e.HTTPSPort), types.Int64Value(e.NativePort)
		f.EndpointPlaintextHTTPPort, f.EndpointPlaintextNativePort = types.Int64PointerValue(e.PlaintextHTTPPort), types.Int64PointerValue(e.PlaintextNativePort)
	}
	f.MaintenanceWindowUTC = types.StringValue(v.Maintenance.WindowUtc)
	f.DeletionProtection = types.BoolValue(v.DeletionProtection)
	f.Status = types.StringValue(v.Status)
}

// fromAPI reads the warehouse into the resource; from_backup and the app user's Secret stay as they were.
func (m *warehouseModel) fromAPI(ctx context.Context, v warehouseView, org string) {
	description := m.Description
	m.fill(ctx, v, org)
	if description.ValueString() == "" && known(description) && v.Description == nil {
		m.Description = description // the API shows an empty description as null
	}
	if !known(m.OperationID) {
		m.OperationID = types.StringValue("")
	}
	if m.OperationTimeout.IsNull() {
		m.OperationTimeout = types.StringValue(defaultTimeout)
	}
}

func readWarehouse(ctx context.Context, c *client.Client, project, id string) (warehouseView, error) {
	var v warehouseView
	out, err := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, "warehouses", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = fmt.Errorf("warehouse response omitted its whs_ identity")
	}
	return v, err
}
