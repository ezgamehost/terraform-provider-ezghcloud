package warehouse

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The names warehouse-api takes (the build brief's Identity table), with its words.
var (
	userName     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	databaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func userNameProblem(name string) string {
	switch {
	case !userName.MatchString(name):
		return "Use 1–32 lowercase letters, digits and underscores, starting with a letter."
	case name == "default" || strings.HasPrefix(name, "ezgh_"):
		return name + " is reserved."
	}
	return ""
}

func databaseNameProblem(name string) string {
	switch {
	case !databaseName.MatchString(name):
		return "Use 1–64 letters, digits and underscores, not starting with a digit."
	case name == "system" || strings.EqualFold(name, "information_schema") || name == "default" || strings.HasPrefix(name, "ezgh_"):
		return name + " is reserved."
	}
	return ""
}

// nameValidator refuses at plan a name the API would refuse.
type nameValidator struct {
	what    string
	problem func(string) string
}

func (v nameValidator) Description(context.Context) string {
	return "a " + v.what + " warehouse-api takes"
}
func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if p := v.problem(req.ConfigValue.ValueString()); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.what, p)
	}
}

// userModel is a warehouse user, addressed by name under its warehouse; the Terraform ID is
// <whs_>/<name>. No attribute holds its password: it's in the Secret.
type userModel struct {
	ID              types.String `tfsdk:"id"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	ProjectID       types.String `tfsdk:"project_id"`
	WarehouseID     types.String `tfsdk:"warehouse_id"`
	Name            types.String `tfsdk:"name"`
	Preset          types.String `tfsdk:"preset"`
	Grants          types.List   `tfsdk:"grants"`
	SettingsProfile types.String `tfsdk:"settings_profile"`
	SecretID        types.String `tfsdk:"secret_id"`
	Status          types.String `tfsdk:"status"`
	FailureCode     types.String `tfsdk:"failure_code"`
	FailureMessage  types.String `tfsdk:"failure_message"`
}
type grant struct {
	Database   types.String `tfsdk:"database"`
	Table      types.String `tfsdk:"table"`
	Privileges types.List   `tfsdk:"privileges"`
}

var grantType = types.ObjectType{AttrTypes: map[string]attr.Type{"database": types.StringType, "table": types.StringType, "privileges": types.ListType{ElemType: types.StringType}}}

type userView struct {
	Name, Status, SettingsProfile string
	Secret                        *string
	Permissions                   struct {
		Preset string
		Grants []struct {
			Database, Table string
			Privileges      []string
		}
	}
	Failure *struct{ Code, Message string }
}

type userResource struct{ client *client.Client }

func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_warehouse_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A user of a warehouse. Its password is generated and kept in a Secret in the warehouse's project; Terraform state never holds it. Changes apply without a restart. Destroying it deletes the user and its Secret. The user app comes with the warehouse: import it to manage it.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "warehouse_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the warehouse. Changing it replaces the user.", PlanModifiers: replace},
		"warehouse_id":    schema.StringAttribute{Required: true, Description: "Warehouse (whs_) it belongs to. Changing it replaces the user.", PlanModifiers: replace},
		"name": schema.StringAttribute{Required: true, Description: "Username: a lowercase letter, then up to 31 lowercase letters, digits and _. default and names starting ezgh_ are reserved. Changing it replaces the user (a new password in a new Secret).", PlanModifiers: replace,
			Validators: []validator.String{nameValidator{"user name", userNameProblem}}},
		"preset": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "admin (everything in every database, every connection, other users' queries), readWrite (select, insert, alter and table DDL in every database), readOnly (select and show) or custom (grants). The API's default when left out at create; removing it later keeps the current preset. Changes in place."},
		"grants": schema.ListNestedAttribute{Optional: true, Description: "With preset custom: grants on databases and tables. Changes in place.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"database":   schema.StringAttribute{Required: true, Description: "A database's name."},
			"table":      schema.StringAttribute{Required: true, Description: "A table's name, or * for every table in the database."},
			"privileges": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "From SELECT, INSERT, ALTER, CREATE, DROP, TRUNCATE, OPTIMIZE and SHOW."},
		}}},
		"settings_profile": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "default, heavy or light: per-query memory and time limits. default when left out at create. Changes in place."},
		"secret_id":        schema.StringAttribute{Computed: true, Description: "The Secret (sec_) holding the user's username and password and how to connect: host, httpsPort, nativePort, the plaintext ports, url and database.", PlanModifiers: keepString},
		"status":           schema.StringAttribute{Computed: true, Description: "What the user is doing: creating, available, modifying, rotating, deleting or failed."},
		"failure_code":     schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed, as a code; null when it didn't."},
		"failure_message":  schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed; null when it didn't."},
	}}
}
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ValidateConfig refuses what the API refuses: grants without preset custom, custom without a
// grant, and a privilege written twice in one grant.
func (r *userResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Preset.IsUnknown() || m.Grants.IsUnknown() {
		return
	}
	custom := m.Preset.ValueString() == "custom"
	some := !m.Grants.IsNull() && len(m.Grants.Elements()) > 0
	switch {
	case some && !custom:
		resp.Diagnostics.AddAttributeError(path.Root("grants"), "grants need preset custom", "Set preset = \"custom\" to give grants.")
	case custom && !some:
		resp.Diagnostics.AddAttributeError(path.Root("grants"), "preset custom needs grants", "Give at least one grant.")
	}
	if !some {
		return
	}
	var grants []grant
	resp.Diagnostics.Append(m.Grants.ElementsAs(ctx, &grants, false)...)
	for i, g := range grants {
		if g.Privileges.IsUnknown() {
			continue
		}
		var ps []string
		resp.Diagnostics.Append(g.Privileges.ElementsAs(ctx, &ps, false)...)
		seen := map[string]bool{}
		for _, p := range ps {
			if seen[p] {
				resp.Diagnostics.AddAttributeError(path.Root("grants").AtListIndex(i).AtName("privileges"), "A privilege written twice", p+" is written twice.")
			}
			seen[p] = true
		}
	}
}

func (m *userModel) path(c *client.Client, extra ...string) string {
	return client.ProjectPath(c.OrganizationID, m.ProjectID.ValueString(), "warehouses", append([]string{m.WarehouseID.ValueString(), "users", m.Name.ValueString()}, extra...)...)
}

// permissions is the request's permissions from what's configured; nil when nothing is, which a
// create leaves to the API.
func (m *userModel) permissions(ctx context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	if !known(m.Preset) {
		return nil, d
	}
	p := map[string]any{"preset": m.Preset.ValueString()}
	if known(m.Grants) && len(m.Grants.Elements()) > 0 {
		var grants []grant
		d.Append(m.Grants.ElementsAs(ctx, &grants, false)...)
		list := []map[string]any{}
		for _, g := range grants {
			var privileges []string
			d.Append(g.Privileges.ElementsAs(ctx, &privileges, false)...)
			list = append(list, map[string]any{"database": g.Database.ValueString(), "table": g.Table.ValueString(), "privileges": privileges})
		}
		p["grants"] = list
	}
	return p, d
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.WarehouseID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	body := map[string]any{"name": m.Name.ValueString()}
	p, d := m.permissions(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if p != nil {
		body["permissions"] = p
	}
	setString(body, "settingsProfile", m.SettingsProfile)
	// Saved only once the API took it: a refused create leaves nothing to destroy.
	saved := func(*client.Response) bool {
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	collection := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "warehouses", m.WarehouseID.ValueString(), "users")
	if err := send(ctx, r.client, "POST", collection, body, saved); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readUser(ctx, r.client, &m)
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	if v.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(m.fill(ctx, v, r.client.OrganizationID)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update changes the permissions and settings profile in place, so the user and its password stay.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{}
	if !m.Preset.Equal(old.Preset) || !m.Grants.Equal(old.Grants) {
		p, d := m.permissions(ctx)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		if p != nil {
			body["permissions"] = p
		}
	}
	if known(m.SettingsProfile) && !m.SettingsProfile.Equal(old.SettingsProfile) {
		body["settingsProfile"] = m.SettingsProfile.ValueString()
	}
	if len(body) > 0 {
		if err := send(ctx, r.client, "PATCH", m.path(r.client), body, nil); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
	}
	r.refresh(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if err := send(ctx, r.client, "DELETE", m.path(r.client), nil, nil); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes organization_id/project_id/whs_…/name.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "whs_") || !prefixedID.MatchString(parts[2]) || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/whs_<12-character ID>/name.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "warehouse_id", "name"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2]+"/"+parts[3])...)
}
func (r *userResource) refresh(ctx context.Context, m *userModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readUser(ctx, r.client, m)
	if err != nil {
		// An error would taint the user, and a replacement would give it a new password.
		m.Status = types.StringValue("available")
		diags.AddWarning("Couldn't read "+m.Name.ValueString(), "Its operation succeeded, but reading it back failed: "+err.Error()+". The next refresh reads it.")
		diags.Append(setKnown(ctx, state, m)...)
		return
	}
	diags.Append(m.fill(ctx, v, r.client.OrganizationID)...)
	diags.Append(state.Set(ctx, m)...)
}
func readUser(ctx context.Context, c *client.Client, m *userModel) (userView, error) {
	var v userView
	out, err := c.Do(ctx, service, "GET", m.path(c), nil)
	if err == nil {
		err = json.Unmarshal(out.Body, &v)
	}
	return v, err
}

// fill reads the user into m. No grants is null, unless configured as [].
func (m *userModel) fill(ctx context.Context, v userView, org string) diag.Diagnostics {
	var d diag.Diagnostics
	m.ID = types.StringValue(m.WarehouseID.ValueString() + "/" + m.Name.ValueString())
	m.OrganizationID = types.StringValue(org)
	m.Preset = types.StringValue(v.Permissions.Preset)
	if !(known(m.Grants) && len(m.Grants.Elements()) == 0) {
		m.Grants = types.ListNull(grantType)
	}
	if len(v.Permissions.Grants) > 0 {
		var list []attr.Value
		for _, g := range v.Permissions.Grants {
			list = append(list, types.ObjectValueMust(grantType.AttrTypes, map[string]attr.Value{"database": types.StringValue(g.Database), "table": types.StringValue(g.Table), "privileges": stringList(ctx, g.Privileges)}))
		}
		m.Grants = types.ListValueMust(grantType, list)
	}
	m.SettingsProfile = types.StringValue(v.SettingsProfile)
	m.SecretID = types.StringPointerValue(v.Secret)
	m.Status = types.StringValue(v.Status)
	m.FailureCode, m.FailureMessage = types.StringNull(), types.StringNull()
	if v.Failure != nil {
		m.FailureCode, m.FailureMessage = types.StringValue(v.Failure.Code), types.StringValue(v.Failure.Message)
	}
	return d
}
