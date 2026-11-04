package databases

import (
	"context"
	"slices"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var presets = []string{"readOnly", "readWrite", "owner"}

type userModel struct {
	childFields
	LogicalDatabase types.String `tfsdk:"logical_database"`
	Preset          types.String `tfsdk:"preset"`
	SecretID        types.String `tfsdk:"secret_id"`
}

type userResource struct{ client *client.Client }

func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := childAttributes("user")
	a["name"] = schema.StringAttribute{Required: true, Description: "Username: a lowercase letter, then up to 31 lowercase letters, digits and underscores. Changing it replaces the user.", PlanModifiers: replace}
	a["logical_database"] = schema.StringAttribute{Required: true, Description: "The logical database the preset grants access to. Changing it replaces the user.", PlanModifiers: replace}
	a["preset"] = schema.StringAttribute{Required: true, Description: "readOnly, readWrite or owner. Changes in place."}
	a["secret_id"] = schema.StringAttribute{Computed: true, Description: "The Secret (sec_) holding the user's username, password, host, port, database, engine and connection uri. Null after a drop the engine refused, until the next apply rotates the user for a new one. Terraform state never holds the password.", PlanModifiers: keepString}
	resp.Schema = schema.Schema{Description: "A user of a database with one preset on one logical database. Its password is generated and kept in a Secret in the project. Destroying it deletes the user and its Secret; tables it created stay.", Attributes: a}
}
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}
func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.Plan.Raw.IsNull() {
		var preset types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("preset"), &preset)...)
		if known(preset) && !slices.Contains(presets, preset.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("preset"), "Invalid preset", "Use readOnly, readWrite or owner.")
		}
	}
	var state userModel
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	planMissing(ctx, req, resp, state.secretless(), "secret_id")
}

// secretless is a user whose drop the engine refused: it's still there with its preset, and its
// Secret is gone until a rotation writes a new one.
func (m *userModel) secretless() bool {
	return m.Status.ValueString() == "available" && m.SecretID.IsNull() && known(m.FailureCode)
}
func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.DatabaseID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	p := childPath(r.client, m.ProjectID.ValueString(), m.DatabaseID.ValueString(), "users")
	body := map[string]any{"name": m.Name.ValueString(), "preset": m.Preset.ValueString(), "logicalDatabase": m.LogicalDatabase.ValueString()}
	// Saved only once the API took it: a refused create (already_exists) leaves nothing to destroy.
	saved := func() bool {
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	if err := m.send(ctx, r.client, "POST", p, body, saved); err != nil {
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
	v, ok := readState(ctx, r.client, &m.childFields, "users", &resp.Diagnostics)
	if !ok {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}
		return
	}
	m.fill(v, r.client.OrganizationID, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update brings a missing user back by rotating its password (which creates it again with its
// preset), or gives a secretless one a new Secret the same way, then PATCHes a changed preset.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if old.Status.ValueString() == "missing" || old.secretless() {
		if err := m.send(ctx, r.client, "POST", m.path(r.client, "users", "rotate-password"), nil, nil); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		// Saved before the PATCH, so a failed PATCH doesn't leave it missing and rotate again.
		rotated := m
		rotated.Preset = old.Preset
		if r.refresh(ctx, &rotated, &resp.State, &resp.Diagnostics); resp.Diagnostics.HasError() {
			return
		}
	}
	if !m.Preset.Equal(old.Preset) {
		if err := m.send(ctx, r.client, "PATCH", m.path(r.client, "users"), map[string]any{"preset": m.Preset.ValueString()}, nil); err != nil {
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
	deleteChild(ctx, r.client, &m.childFields, "users", &resp.Diagnostics)
}
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, r.client, req, resp, "users", "users")
}
func (r *userResource) refresh(ctx context.Context, m *userModel, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readChild(ctx, r.client, &m.childFields, "users")
	if err != nil {
		unread(ctx, &m.childFields, m, state, diags, err)
		return
	}
	m.fill(v, r.client.OrganizationID, true)
	diags.Append(state.Set(ctx, m)...)
}
func (m *userModel) fill(v childView, org string, afterOperation bool) {
	m.childFields.fill(v, org, afterOperation)
	m.LogicalDatabase = types.StringPointerValue(v.LogicalDatabase)
	m.Preset = types.StringPointerValue(v.Preset)
	m.SecretID = types.StringPointerValue(v.Secret)
}
