package caches

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var presets = []string{"readWrite", "readOnly"}

// userModel is a cache user, addressed by name under its cache; the Terraform ID is <cch_>/<name>.
// No attribute holds its password: it's in the Secret.
type userModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	CacheID        types.String `tfsdk:"cache_id"`
	Name           types.String `tfsdk:"name"`
	Preset         types.String `tfsdk:"preset"`
	SecretID       types.String `tfsdk:"secret_id"`
	Status         types.String `tfsdk:"status"`
	FailureCode    types.String `tfsdk:"failure_code"`
	FailureMessage types.String `tfsdk:"failure_message"`
}
type userView struct {
	Name, Status   string
	Preset, Secret *string
	Failure        *struct{ Code, Message string }
}

type userResource struct{ client *client.Client }

func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cache_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A user of a cache. Its password is generated and kept in a Secret in the cache's project; Terraform state never holds it. Destroying it deletes the user and its Secret. The user default comes with the cache and can't be destroyed.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "cache_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the cache. Changing it replaces the user.", PlanModifiers: replace},
		"cache_id":        schema.StringAttribute{Required: true, Description: "Cache (cch_) it belongs to. Changing it replaces the user.", PlanModifiers: replace},
		"name":            schema.StringAttribute{Required: true, Description: "Username: a lowercase letter, then up to 31 lowercase letters, digits, _ and -. ezgh_ names are reserved. Changing it replaces the user.", PlanModifiers: replace},
		"preset":          schema.StringAttribute{Optional: true, Computed: true, Description: "Valkey only: readWrite or readOnly (readWrite when left out at create; removing it later keeps the current preset). Changes in place. Null on Memcached, whose users have full access.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"secret_id":       schema.StringAttribute{Computed: true, Description: "The Secret (sec_) holding the user's username, password, host, port, engine and, on Valkey, a rediss:// uri.", PlanModifiers: keepString},
		"status":          schema.StringAttribute{Computed: true, Description: "What the user is doing: creating, available, modifying, rotating, deleting or failed."},
		"failure_code":    schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed, as a code; null when it didn't."},
		"failure_message": schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed; null when it didn't."},
	}}
}
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}
func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var preset types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("preset"), &preset)...)
	if known(preset) && !slices.Contains(presets, preset.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("preset"), "Invalid preset", "Use readWrite or readOnly.")
	}
}

func (m *userModel) path(c *client.Client, extra ...string) string {
	return client.ProjectPath(c.OrganizationID, m.ProjectID.ValueString(), "caches", append([]string{m.CacheID.ValueString(), "users", m.Name.ValueString()}, extra...)...)
}

// send makes one async call and waits for its operation. accepted, when set, runs once the API has
// taken the call and before the wait.
func send(ctx context.Context, c *client.Client, method, p string, body any, accepted func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	out, err := c.Do(ctx, service, method, p, body)
	if err != nil {
		return err
	}
	if accepted != nil && !accepted() {
		return fmt.Errorf("the user was accepted but couldn't be saved to state; import it")
	}
	if err = waitResponse(ctx, c, out); err != nil {
		return fmt.Errorf("operation %s: %w", operationID(out).ValueString(), err)
	}
	return nil
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.CacheID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	body := map[string]any{"name": m.Name.ValueString()}
	setString(body, "preset", m.Preset)
	// Saved only once the API took it: a refused create leaves nothing to destroy.
	saved := func() bool {
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "caches", m.CacheID.ValueString(), "users")
	if err := send(ctx, r.client, "POST", p, body, saved); err != nil {
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
	m.fill(v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update changes the preset in place, so the user and its password stay.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if known(m.Preset) && !m.Preset.Equal(old.Preset) {
		if err := send(ctx, r.client, "PATCH", m.path(r.client), map[string]any{"preset": m.Preset.ValueString()}, nil); err != nil {
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
		var e *client.Error
		if errors.As(err, &e) && e.Code == "default_user" {
			resp.Diagnostics.AddWarning("The default user stays", "The user default goes with its cache, so destroying it only removes it from Terraform's state.")
			return
		}
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes organization_id/project_id/cch_…/name.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "cch_") || !prefixedID.MatchString(parts[2]) || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/cch_<12-character ID>/name.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "cache_id", "name"} {
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
	m.fill(v, r.client.OrganizationID)
	diags.Append(state.Set(ctx, m)...)
}
func readUser(ctx context.Context, c *client.Client, m *userModel) (userView, error) {
	var v userView
	out, err := c.Do(ctx, service, "GET", m.path(c), nil)
	if err == nil {
		err = out.Decode(&v)
	}
	return v, err
}
func (m *userModel) fill(v userView, org string) {
	m.ID = types.StringValue(m.CacheID.ValueString() + "/" + m.Name.ValueString())
	m.OrganizationID = types.StringValue(org)
	m.Preset = types.StringPointerValue(v.Preset)
	m.SecretID = types.StringPointerValue(v.Secret)
	m.Status = types.StringValue(v.Status)
	m.FailureCode, m.FailureMessage = types.StringNull(), types.StringNull()
	if v.Failure != nil {
		m.FailureCode, m.FailureMessage = types.StringValue(v.Failure.Code), types.StringValue(v.Failure.Message)
	}
}
