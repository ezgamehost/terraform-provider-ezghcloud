package databases

import (
	"context"
	"fmt"
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

// childFields are what logical databases and users share. Both live under a database and are
// addressed by name; the Terraform ID is <dbi_>/<name>.
type childFields struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	DatabaseID     types.String `tfsdk:"database_id"`
	Name           types.String `tfsdk:"name"`
	Status         types.String `tfsdk:"status"`
	FailureCode    types.String `tfsdk:"failure_code"`
	FailureMessage types.String `tfsdk:"failure_message"`
}
type childView struct {
	Database, Name                  string
	Managed                         bool
	Status                          *string
	Preset, LogicalDatabase, Secret *string
	Failure                         *struct{ Code, Message string }
}

var replace = []planmodifier.String{stringplanmodifier.RequiresReplace()}

func childAttributes(noun string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "database_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the database. Changing it replaces the " + noun + ".", PlanModifiers: replace},
		"database_id":     schema.StringAttribute{Required: true, Description: "Database (dbi_) it lives in. Changing it replaces the " + noun + ".", PlanModifiers: replace},
		// Planned as available when it reads missing, so the apply brings it back.
		"status": schema.StringAttribute{Computed: true, Description: "What the " + noun + " is doing: creating, available, missing (absent from the engine, for example dropped in SQL), failed, and others while it changes."},
		// No plan modifier: ModifyPlan plans them null on an update, since every operation that succeeds clears them.
		"failure_code":    schema.StringAttribute{Computed: true, Description: "Why the " + noun + "'s last operation failed, as a code; null when it didn't."},
		"failure_message": schema.StringAttribute{Computed: true, Description: "Why the " + noun + "'s last operation failed; null when it didn't."},
	}
}

func childPath(c *client.Client, project, database, collection string, name ...string) string {
	return client.ProjectPath(c.OrganizationID, project, "databases", append([]string{database, collection}, name...)...)
}
func (f *childFields) path(c *client.Client, collection string, extra ...string) string {
	return childPath(c, f.ProjectID.ValueString(), f.DatabaseID.ValueString(), collection, append([]string{f.Name.ValueString()}, extra...)...)
}
func readChild(ctx context.Context, c *client.Client, f *childFields, collection string) (childView, error) {
	var v childView
	out, err := c.Do(ctx, service, "GET", f.path(c, collection), nil)
	if err == nil {
		err = out.Decode(&v)
	}
	return v, err
}

// fill reads v into f. A managed object our own operation just finished on can still read missing
// until the node's next report lands, so afterOperation reads that as available.
func (f *childFields) fill(v childView, org string, afterOperation bool) {
	f.ID = types.StringValue(f.DatabaseID.ValueString() + "/" + f.Name.ValueString())
	f.OrganizationID = types.StringValue(org)
	f.Status = types.StringPointerValue(v.Status)
	f.FailureCode, f.FailureMessage = types.StringNull(), types.StringNull()
	if v.Failure != nil {
		f.FailureCode, f.FailureMessage = types.StringValue(v.Failure.Code), types.StringValue(v.Failure.Message)
	}
	if afterOperation && f.Status.ValueString() == "missing" {
		f.Status = types.StringValue("available")
	}
}

// send makes one async call for the object and waits for its operation. accepted, when set, runs
// once the API has taken the call and before the wait; returning false stops there with an error.
func (f *childFields) send(ctx context.Context, c *client.Client, method, path string, body any, accepted func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Hour) // defaultTimeout; these operations take seconds
	defer cancel()
	out, err := c.Do(ctx, service, method, path, body)
	if err != nil {
		return err
	}
	if accepted != nil && !accepted() {
		return fmt.Errorf("%s was accepted but couldn't be saved to state; import it", f.Name.ValueString())
	}
	if err = waitResponse(ctx, c, out); err != nil {
		return fmt.Errorf("%s, operation %s: %w", f.Name.ValueString(), operationID(out).ValueString(), err)
	}
	return nil
}

// readState refreshes a child in Read: a 404, an object being deleted, or a name only made in SQL
// is gone from Terraform's view.
func readState(ctx context.Context, c *client.Client, f *childFields, collection string, diags *diag.Diagnostics) (childView, bool) {
	v, err := readChild(ctx, c, f, collection)
	if err != nil {
		missing, e := confirmedMissing(ctx, c, f.ProjectID.ValueString(), err)
		if !missing {
			apiError(diags, e)
		}
		return v, false
	}
	status := ""
	if v.Status != nil {
		status = *v.Status
	}
	return v, v.Managed && status != "deleting" && status != "deleted"
}

// planMissing plans status available for a missing object, so the plan shows an in-place update and
// the apply recreates it (DU4: only the next change made through EZGH touches it again). The
// recreate can change each of unknown (a user's Secret, if the customer deleted it). Any update
// plans the failure null: each one runs an operation, and one that succeeds clears it.
func planMissing(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, unknown ...string) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() || len(resp.RequiresReplace) > 0 {
		return
	}
	var status types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("status"), &status)...)
	if status.ValueString() == "missing" {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), "available")...)
		for _, name := range unknown {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
		}
	}
	if !resp.Plan.Raw.Equal(req.State.Raw) {
		for _, name := range []string{"failure_code", "failure_message"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringNull())...)
		}
	}
}

// unread handles a failed read after an operation succeeded: an error would taint the object, and
// replacing it would drop it (or give a user a new password). It keeps what's known, available, and
// the next refresh reads the rest.
func unread(ctx context.Context, f *childFields, m any, state *tfsdk.State, diags *diag.Diagnostics, err error) {
	f.Status = types.StringValue("available")
	diags.AddWarning("Couldn't read "+f.Name.ValueString(), "Its operation succeeded, but reading it back failed: "+err.Error()+". The next refresh reads it.")
	diags.Append(setKnown(ctx, state, m)...)
}
func deleteChild(ctx context.Context, c *client.Client, f *childFields, collection string, diags *diag.Diagnostics) {
	if err := f.send(ctx, c, "DELETE", f.path(c, collection), nil, nil); err != nil {
		if missing, e := confirmedMissing(ctx, c, f.ProjectID.ValueString(), err); !missing {
			apiError(diags, e)
		}
	}
}

// importChild takes organization_id/project_id/dbi_…/name and refuses a name made only in SQL.
func importChild(ctx context.Context, c *client.Client, req resource.ImportStateRequest, resp *resource.ImportStateResponse, collection, noun string) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "dbi_") || !prefixedID.MatchString(parts[2]) || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/dbi_<12-character ID>/name.")
		return
	}
	if c != nil && parts[0] != c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	f := childFields{ProjectID: types.StringValue(parts[1]), DatabaseID: types.StringValue(parts[2]), Name: types.StringValue(parts[3])}
	if c != nil {
		v, err := readChild(ctx, c, &f, collection)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		if !v.Managed {
			resp.Diagnostics.AddError("Can't import "+parts[3], "Only "+noun+" created through EZGH can be imported. "+parts[3]+" was made in SQL.")
			return
		}
	}
	for i, k := range []string{"organization_id", "project_id", "database_id", "name"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2]+"/"+parts[3])...)
}

type logicalDatabaseResource struct{ client *client.Client }

func (*logicalDatabaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_logical_database"
}
func (*logicalDatabaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := childAttributes("logical database")
	a["name"] = schema.StringAttribute{Required: true, Description: "Name in the engine: a lowercase letter, then up to 62 lowercase letters, digits and underscores. Changing it replaces the logical database.", PlanModifiers: replace}
	resp.Schema = schema.Schema{Description: "A logical database inside a database, such as an app's own database. Destroying it drops its data.", Attributes: a}
}
func (r *logicalDatabaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}
func (r *logicalDatabaseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	planMissing(ctx, req, resp)
}
func (r *logicalDatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m childFields
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.create(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *logicalDatabaseResource) create(ctx context.Context, m *childFields, state *tfsdk.State, diags *diag.Diagnostics) {
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.DatabaseID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	p := childPath(r.client, m.ProjectID.ValueString(), m.DatabaseID.ValueString(), "logical-databases")
	// Saved only once the API took it: a refused create (already_exists) leaves nothing to destroy.
	saved := func() bool { diags.Append(setKnown(ctx, state, m)...); return !diags.HasError() }
	if err := m.send(ctx, r.client, "POST", p, map[string]any{"name": m.Name.ValueString()}, saved); err != nil {
		apiError(diags, err)
		return
	}
	r.refresh(ctx, m, state, diags)
}
func (r *logicalDatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m childFields
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, ok := readState(ctx, r.client, &m, "logical-databases", &resp.Diagnostics)
	if !ok {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}
		return
	}
	m.fill(v, r.client.OrganizationID, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update runs only when a missing logical database is planned back to available: it creates it again.
func (r *logicalDatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m childFields
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.create(ctx, &m, &resp.State, &resp.Diagnostics)
}
func (r *logicalDatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m childFields
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	deleteChild(ctx, r.client, &m, "logical-databases", &resp.Diagnostics)
}
func (r *logicalDatabaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, r.client, req, resp, "logical-databases", "logical databases")
}
func (r *logicalDatabaseResource) refresh(ctx context.Context, m *childFields, state *tfsdk.State, diags *diag.Diagnostics) {
	v, err := readChild(ctx, r.client, m, "logical-databases")
	if err != nil {
		unread(ctx, m, m, state, diags, err)
		return
	}
	m.fill(v, r.client.OrganizationID, true)
	diags.Append(state.Set(ctx, m)...)
}
