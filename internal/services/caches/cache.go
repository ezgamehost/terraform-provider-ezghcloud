package caches

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// cacheFields are what the API shows; the data source reads exactly these.
type cacheFields struct {
	ID                    types.String `tfsdk:"id"`
	OrganizationID        types.String `tfsdk:"organization_id"`
	ProjectID             types.String `tfsdk:"project_id"`
	ResourceName          types.String `tfsdk:"resource_name"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	Tags                  types.Map    `tfsdk:"tags"`
	Engine                types.String `tfsdk:"engine"`
	EngineVersion         types.String `tfsdk:"engine_version"`
	Mode                  types.String `tfsdk:"mode"`
	InstanceType          types.String `tfsdk:"instance_type"`
	Standby               types.Bool   `tfsdk:"standby"`
	StorageSizeGiB        types.Int64  `tfsdk:"storage_size_gib"`
	StorageIOPS           types.Int64  `tfsdk:"storage_iops"`
	StorageThroughputMBps types.Int64  `tfsdk:"storage_throughput_mbps"`
	VPCID                 types.String `tfsdk:"vpc_id"`
	SubnetID              types.String `tfsdk:"subnet_id"`
	AllowedSourceCIDRs    types.List   `tfsdk:"allowed_source_cidrs"`
	PlaintextPort         types.Bool   `tfsdk:"plaintext_port"`
	EndpointHostname      types.String `tfsdk:"endpoint_hostname"`
	EndpointPort          types.Int64  `tfsdk:"endpoint_port"`
	EndpointPlaintextPort types.Int64  `tfsdk:"endpoint_plaintext_port"`
	MaintenanceWindowUTC  types.String `tfsdk:"maintenance_window_utc"`
	Parameters            types.Map    `tfsdk:"parameters"`
	DeletionProtection    types.Bool   `tfsdk:"deletion_protection"`
	Status                types.String `tfsdk:"status"`
}
type cacheModel struct {
	cacheFields
	OperationID      types.String `tfsdk:"operation_id"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}
type cacheView struct {
	Slug, ResourceName, Name, Engine, EngineVersion, Mode, InstanceType, VPC, Subnet, Status string
	Description                                                                              *string
	Tags, Parameters                                                                         map[string]string
	Standby, DeletionProtection                                                              bool
	PlaintextPort                                                                            *bool
	Storage                                                                                  *struct{ SizeGiB, IOPS, ThroughputMBps int64 }
	AllowedSourceCidrs                                                                       []string
	Endpoint                                                                                 *struct {
		Hostname      string
		Port          int64
		PlaintextPort *int64
	}
	Maintenance struct{ WindowUtc string }
}

type cacheResource struct{ client *client.Client }

func (*cacheResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cache"
}

var (
	keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt    = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	replace    = []planmodifier.String{stringplanmodifier.RequiresReplace()}
)

func (*cacheResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: d, PlanModifiers: keepString}
	}
	optionalInt := func(d string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Description: d, PlanModifiers: keepInt}
	}
	resp.Schema = schema.Schema{Description: "A managed Valkey or Memcached cache in a subnet of your VPC, reached at one endpoint over TLS with a username and password. It comes with the user default, whose password is in a Secret.", Attributes: map[string]schema.Attribute{
		"id":                      computed("Cache ID (cch_)."),
		"organization_id":         computed("Organization managed by the configured provider."),
		"project_id":              schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the cache.", PlanModifiers: replace},
		"resource_name":           computed("IAM resource name."),
		"name":                    schema.StringAttribute{Required: true, Description: "Cache name. Changes in place."},
		"description":             schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."},
		"tags":                    schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"engine":                  schema.StringAttribute{Required: true, Description: "valkey or memcached. Changing it replaces the cache.", PlanModifiers: replace},
		"engine_version":          schema.StringAttribute{Required: true, Description: "Engine version: Valkey 8.1 or 9.1, Memcached 1.6. A newer version upgrades in place (one-way); an older one is refused at plan. On Memcached an upgrade is applied in the maintenance window and the cache starts empty."},
		"mode":                    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("memory"), Description: "memory or persistent (Valkey only, with storage_size_gib). Changing it replaces the cache.", PlanModifiers: replace},
		"instance_type":           schema.StringAttribute{Required: true, Description: "A gp-1 or gs-1 instance type. Changes in place."},
		"standby":                 schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "A standby that takes over if the cache fails: on Valkey with a live copy of the data, on Memcached empty. Changes in place."},
		"storage_size_gib":        schema.Int64Attribute{Optional: true, Description: "A persistent cache's storage in GiB, required for one and refused otherwise. Grows in place; shrinking is refused."},
		"storage_iops":            optionalInt("A persistent cache's provisioned storage IOPS. Changes in place."),
		"storage_throughput_mbps": optionalInt("A persistent cache's provisioned storage throughput in MB/s. Changes in place."),
		"vpc_id":                  computed("VPC of the subnet."),
		"subnet_id":               schema.StringAttribute{Required: true, Description: "Subnet (sbn_) the cache is in: it needs 2 free addresses, 3 with a standby. Changing it replaces the cache.", PlanModifiers: replace},
		"allowed_source_cidrs":    schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "1 to 20 IPv4 CIDRs allowed to connect. Left out at create, the VPC's ranges; removing it later keeps the current list.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"plaintext_port":          schema.BoolAttribute{Optional: true, Computed: true, Description: "Memcached only: also serve port 11212 without TLS, with a username and password (true when left out at create; removing it later keeps the current value). Null on Valkey, which is TLS only. Changes in place.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"endpoint_hostname":       computed("The endpoint's DNS name; null until it resolves. It stays the same through failovers and upgrades."),
		"endpoint_port":           schema.Int64Attribute{Computed: true, Description: "The endpoint's TLS port: 6379 on Valkey, 11211 on Memcached.", PlanModifiers: keepInt},
		"endpoint_plaintext_port": schema.Int64Attribute{Computed: true, Description: "Memcached's port without TLS, 11212, while plaintext_port is true; null otherwise."},
		"maintenance_window_utc":  schema.StringAttribute{Optional: true, Computed: true, Description: "Weekly maintenance window, ddd:HH:MM-ddd:HH:MM UTC, at least 30 minutes.", PlanModifiers: keepString},
		"parameters":              schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Engine parameters, such as maxmemory-policy. Only configured keys are sent and read back; removing a key leaves its value as it is."},
		"deletion_protection":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "While true, the cache can't be destroyed."},
		"status":                  schema.StringAttribute{Computed: true, Description: "What the cache is doing."},
		"operation_id":            schema.StringAttribute{Computed: true, Description: "Most recent operation initiated by this resource; unavailable on import."},
		"operation_timeout":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultTimeout), Description: "Maximum wait for each asynchronous operation, for example 60m."},
	}}
}
func (r *cacheResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// compareVersions orders "8.1" < "9.1" < "9.10", each dotted part as a number.
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

func (r *cacheResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan cacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(plan.Mode) && known(plan.Engine) {
		persistent := plan.Mode.ValueString() == "persistent"
		switch {
		case persistent && plan.Engine.ValueString() != "valkey":
			resp.Diagnostics.AddAttributeError(path.Root("mode"), "Persistent is Valkey's", "Memcached caches are memory only.")
		case persistent && plan.StorageSizeGiB.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("storage_size_gib"), "Storage is required", "A persistent cache needs storage_size_gib.")
		case !persistent && known(plan.StorageSizeGiB):
			resp.Diagnostics.AddAttributeError(path.Root("storage_size_gib"), "Storage is for persistent caches", "A memory-only cache keeps nothing on disk: leave storage_size_gib out.")
		}
	}
	if known(plan.Mode) && plan.Mode.ValueString() != "persistent" && !req.Config.Raw.IsNull() {
		for _, name := range []string{"storage_iops", "storage_throughput_mbps"} {
			var configured types.Int64
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &configured)...)
			if !configured.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root(name), "Storage is for persistent caches", "A memory-only cache keeps nothing on disk: leave "+name+" out.")
			}
		}
	}
	if known(plan.Engine) && plan.Engine.ValueString() == "valkey" && known(plan.PlaintextPort) && !req.Config.Raw.IsNull() {
		var configured types.Bool
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("plaintext_port"), &configured)...)
		if !configured.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("plaintext_port"), "plaintext_port is Memcached's", "Valkey is reached over TLS only: leave plaintext_port out.")
		}
	}
	// A replacement makes a new cache: an older version or less storage is fine then.
	if req.State.Raw.IsNull() || len(resp.RequiresReplace) > 0 {
		return
	}
	var state cacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(plan.EngineVersion) && compareVersions(plan.EngineVersion.ValueString(), state.EngineVersion.ValueString()) < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("engine_version"), "engine_version can't go back",
			fmt.Sprintf("Upgrades are one-way: %s can't move to the older %s.", state.EngineVersion.ValueString(), plan.EngineVersion.ValueString()))
	}
	if known(plan.StorageSizeGiB) && known(state.StorageSizeGiB) && plan.StorageSizeGiB.ValueInt64() < state.StorageSizeGiB.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("storage_size_gib"), "Storage can't shrink", fmt.Sprintf("storage_size_gib must be at least %d.", state.StorageSizeGiB.ValueInt64()))
	}
}

func (r *cacheResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m cacheModel
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
	out, err := r.client.Do(ctx, service, "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "caches"), body)
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
			resp.Diagnostics.AddWarning("Cache still being created", fmt.Sprintf("Cache %s was still being created when the wait ended. The next refresh or apply waits for operation %s.", id, m.OperationID.ValueString()))
			return
		}
		apiError(&resp.Diagnostics, fmt.Errorf("cache %s, operation %s: %w", id, m.OperationID.ValueString(), err))
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

// createBody is POST /caches': mode and storage are Valkey's, plaintextPort Memcached's, and what's
// unset is left to the API.
func createBody(ctx context.Context, m cacheModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	tags, td := stringMap(ctx, m.Tags)
	d.Append(td...)
	body := map[string]any{"name": m.Name.ValueString(), "tags": tags, "engine": m.Engine.ValueString(), "engineVersion": m.EngineVersion.ValueString(),
		"instanceType": m.InstanceType.ValueString(), "standby": m.Standby.ValueBool(), "subnet": m.SubnetID.ValueString(), "deletionProtection": m.DeletionProtection.ValueBool()}
	setString(body, "description", m.Description)
	if m.Engine.ValueString() == "valkey" {
		setString(body, "mode", m.Mode)
	} else {
		setBool(body, "plaintextPort", m.PlaintextPort)
	}
	if known(m.StorageSizeGiB) {
		storage := map[string]any{"sizeGiB": m.StorageSizeGiB.ValueInt64()}
		setInt(storage, "iops", m.StorageIOPS)
		setInt(storage, "throughputMBps", m.StorageThroughputMBps)
		body["storage"] = storage
	}
	if known(m.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	if known(m.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.Parameters) {
		params, pd := stringMap(ctx, m.Parameters)
		d.Append(pd...)
		body["parameters"] = params
	}
	return body, d
}

// updateBody is the PATCH for everything but an upgrade: only what changed, since caches-api refuses
// a body naming a setting it can't change during a blue/green deployment. Parameters sends only
// changed keys: the API changes only those it names.
func updateBody(ctx context.Context, m, old cacheModel) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	tags, td := stringMap(ctx, m.Tags)
	d.Append(td...)
	body := map[string]any{}
	if !m.Name.Equal(old.Name) {
		body["name"] = m.Name.ValueString()
	}
	if !m.Description.Equal(old.Description) {
		body["description"] = m.Description.ValueStringPointer()
	}
	if !m.Tags.Equal(old.Tags) {
		body["tags"] = tags
	}
	if !m.DeletionProtection.Equal(old.DeletionProtection) {
		body["deletionProtection"] = m.DeletionProtection.ValueBool()
	}
	if !m.InstanceType.Equal(old.InstanceType) {
		body["instanceType"] = m.InstanceType.ValueString()
	}
	if !m.Standby.Equal(old.Standby) {
		body["standby"] = m.Standby.ValueBool()
	}
	if known(m.StorageSizeGiB) && (!m.StorageSizeGiB.Equal(old.StorageSizeGiB) || !m.StorageIOPS.Equal(old.StorageIOPS) || !m.StorageThroughputMBps.Equal(old.StorageThroughputMBps)) {
		storage := map[string]any{}
		if !m.StorageSizeGiB.Equal(old.StorageSizeGiB) {
			storage["sizeGiB"] = m.StorageSizeGiB.ValueInt64()
		}
		if !m.StorageIOPS.Equal(old.StorageIOPS) {
			setInt(storage, "iops", m.StorageIOPS)
		}
		if !m.StorageThroughputMBps.Equal(old.StorageThroughputMBps) {
			setInt(storage, "throughputMBps", m.StorageThroughputMBps)
		}
		body["storage"] = storage
	}
	if known(m.AllowedSourceCIDRs) && !m.AllowedSourceCIDRs.Equal(old.AllowedSourceCIDRs) {
		var cidrs []string
		d.Append(m.AllowedSourceCIDRs.ElementsAs(ctx, &cidrs, false)...)
		body["allowedSourceCidrs"] = cidrs
	}
	if known(m.PlaintextPort) && !m.PlaintextPort.Equal(old.PlaintextPort) {
		body["plaintextPort"] = m.PlaintextPort.ValueBool()
	}
	if known(m.MaintenanceWindowUTC) && !m.MaintenanceWindowUTC.Equal(old.MaintenanceWindowUTC) {
		body["maintenance"] = map[string]any{"windowUtc": m.MaintenanceWindowUTC.ValueString()}
	}
	if known(m.Parameters) && !m.Parameters.Equal(old.Parameters) {
		planned, pd := stringMap(ctx, m.Parameters)
		d.Append(pd...)
		before := map[string]string{}
		if known(old.Parameters) {
			before, pd = stringMap(ctx, old.Parameters)
			d.Append(pd...)
		}
		changed := map[string]string{}
		for k, v := range planned {
			if before[k] != v {
				changed[k] = v
			}
		}
		if len(changed) > 0 {
			body["parameters"] = changed
		}
	}
	return body, d
}

func (r *cacheResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m cacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readCache(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	pending := m.EngineVersion
	m.fromAPI(ctx, v, r.client.OrganizationID)
	if v.Status == "upgrading" && v.Engine == "memcached" && known(pending) {
		m.EngineVersion = pending // an upgrade waiting for the maintenance window
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if v.Status != "creating" {
		return
	}
	if m.OperationID.ValueString() == "" {
		resp.Diagnostics.AddError("Cache operation in progress", "The cache is "+v.Status+" and its operation ID is unavailable. Retry refresh after the operation completes.")
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

// Update sends the settings that changed, then an upgrade on its own (the API takes engineVersion
// alone).
func (r *cacheResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old cacheModel
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
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "caches", m.ID.ValueString())
	m.OperationID, m.Status = old.OperationID, old.Status
	send := func(b map[string]any, wait bool) bool {
		out, err := r.client.Do(ctx, service, "PATCH", p, b)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return false
		}
		m.OperationID = operationID(out)
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		if !wait {
			return true
		}
		if err = waitResponse(ctx, r.client, out); err != nil {
			apiError(&resp.Diagnostics, fmt.Errorf("cache %s, operation %s: %w", m.ID.ValueString(), m.OperationID.ValueString(), err))
			return false
		}
		return true
	}
	if len(body) > 0 && !send(body, true) {
		return
	}
	if !m.EngineVersion.Equal(old.EngineVersion) {
		// Memcached's upgrade waits for the maintenance window: it's accepted, not waited for, and
		// state keeps the new version while the cache is upgrading.
		memcached := m.Engine.ValueString() == "memcached"
		if !send(map[string]any{"engineVersion": m.EngineVersion.ValueString()}, !memcached) {
			return
		}
		if memcached {
			resp.Diagnostics.AddWarning("Upgrade applied in the maintenance window", fmt.Sprintf("Cache %s moves to Memcached %s in its maintenance window (%s), and starts empty then. Its operation is %s.", m.ID.ValueString(), m.EngineVersion.ValueString(), m.MaintenanceWindowUTC.ValueString(), m.OperationID.ValueString()))
			m.Status = types.StringValue("upgrading")
			resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
			return
		}
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}

func (r *cacheResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m cacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Cache deletion protection enabled", "Set deletion_protection to false and apply before destroying this cache.")
		return
	}
	ctx, cancel := timeoutContext(ctx, m.OperationTimeout, &resp.Diagnostics)
	defer cancel()
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "caches", m.ID.ValueString())
	out, err := r.client.Do(ctx, service, "DELETE", p, nil)
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
	_, err = readCache(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Cache deletion incomplete", "The operation succeeded but the cache is still returned by its API. Refresh and retry.")
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *cacheResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "cch_")
}

func (r *cacheResource) refresh(ctx context.Context, m *cacheModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readCache(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		apiError(diags, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	diags.Append(state.Set(ctx, m)...)
}

func (f *cacheFields) fill(ctx context.Context, v cacheView, org string) {
	f.ID = types.StringValue(v.Slug)
	f.OrganizationID = types.StringValue(org)
	f.ResourceName = types.StringValue(v.ResourceName)
	f.Name = types.StringValue(v.Name)
	f.Description = types.StringPointerValue(v.Description)
	f.Tags = mapValue(ctx, v.Tags)
	f.Engine = types.StringValue(v.Engine)
	f.EngineVersion = types.StringValue(v.EngineVersion)
	f.Mode = types.StringValue(v.Mode)
	f.InstanceType = types.StringValue(v.InstanceType)
	f.Standby = types.BoolValue(v.Standby)
	f.StorageSizeGiB, f.StorageIOPS, f.StorageThroughputMBps = types.Int64Null(), types.Int64Null(), types.Int64Null()
	if v.Storage != nil {
		f.StorageSizeGiB, f.StorageIOPS, f.StorageThroughputMBps = types.Int64Value(v.Storage.SizeGiB), types.Int64Value(v.Storage.IOPS), types.Int64Value(v.Storage.ThroughputMBps)
	}
	f.VPCID = types.StringValue(v.VPC)
	f.SubnetID = types.StringValue(v.Subnet)
	f.AllowedSourceCIDRs = stringList(ctx, v.AllowedSourceCidrs)
	f.PlaintextPort = types.BoolPointerValue(v.PlaintextPort)
	f.EndpointHostname, f.EndpointPort, f.EndpointPlaintextPort = types.StringNull(), types.Int64Null(), types.Int64Null()
	if v.Endpoint != nil {
		f.EndpointHostname, f.EndpointPort, f.EndpointPlaintextPort = types.StringValue(v.Endpoint.Hostname), types.Int64Value(v.Endpoint.Port), types.Int64PointerValue(v.Endpoint.PlaintextPort)
	}
	f.MaintenanceWindowUTC = types.StringValue(v.Maintenance.WindowUtc)
	f.Parameters = mapValue(ctx, v.Parameters)
	f.DeletionProtection = types.BoolValue(v.DeletionProtection)
	f.Status = types.StringValue(v.Status)
}

// fromAPI reads the cache into the resource. parameters keeps the configured keys only, so a default
// the API shows isn't drift; an import reads none of them.
func (m *cacheModel) fromAPI(ctx context.Context, v cacheView, org string) {
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
}

func readCache(ctx context.Context, c *client.Client, project, id string) (cacheView, error) {
	var v cacheView
	out, err := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, "caches", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = fmt.Errorf("cache response omitted its cch_ identity")
	}
	return v, err
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
func setBool(body map[string]any, key string, v types.Bool) {
	if known(v) {
		body[key] = v.ValueBool()
	}
}
func stringList(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	out, _ := types.ListValueFrom(ctx, types.StringType, v)
	return out
}
