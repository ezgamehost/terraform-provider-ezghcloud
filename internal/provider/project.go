package provider

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type projectModel struct {
	ID           types.String `tfsdk:"id"`
	Slug         types.String `tfsdk:"slug"`
	Organization types.String `tfsdk:"organization_id"`
	Name         types.String `tfsdk:"name"`
	Folder       types.String `tfsdk:"folder_id"`
}
type projectResource struct{ c *client.Client }
type projectWire struct {
	ID, Slug, Name string
	FolderID       *string `json:"folderId"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}
func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{Description: "An organization project. Deleting a project removes its access policies and starts its product cleanup; destroy contained resources first.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, PlanModifiers: stable, Description: "Project API identifier. Use this value in project_id attributes."},
		"slug":            schema.StringAttribute{Computed: true, PlanModifiers: stable, Description: "Canonical prj_ identifier used in IAM resource names."},
		"organization_id": schema.StringAttribute{Computed: true, PlanModifiers: stable, Description: "Organization of this project, from the provider configuration."},
		"name":            schema.StringAttribute{Required: true, Description: "Project name."},
		"folder_id":       schema.StringAttribute{Optional: true, Description: "Optional folder identifier; null places the project at the organization root."},
	}}
}
func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.c, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "Expected the EZGH Cloud API client.")
	}
}
func (m projectModel) body() map[string]any {
	var folder any
	if !m.Folder.IsNull() {
		folder = m.Folder.ValueString()
	}
	return map[string]any{"name": m.Name.ValueString(), "folderId": folder}
}
func (m *projectModel) decode(resp *client.Response) error {
	var w projectWire
	if err := resp.Decode(&w); err != nil {
		return err
	}
	if w.ID == "" || w.Slug == "" || w.Name == "" {
		return errors.New("project response omitted identity or name")
	}
	m.ID = types.StringValue(w.ID)
	m.Slug = types.StringValue(w.Slug)
	m.Name = types.StringValue(w.Name)
	m.Folder = types.StringPointerValue(w.FolderID)
	return nil
}
func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.Organization = types.StringValue(r.c.OrganizationID)
	out, err := r.c.Do(ctx, "orgs", "POST", client.OrgPath(r.c.OrganizationID, "projects"), m.body())
	if err == nil {
		err = m.decode(out)
	}
	if err != nil {
		resp.Diagnostics.AddError("Create project failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Organization.ValueString() != r.c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "This project belongs to another provider organization.")
		return
	}
	out, err := r.c.Do(ctx, "orgs", "GET", client.OrgPath(r.c.OrganizationID, "projects", m.ID.ValueString()), nil)
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		if r.organizationVisible(ctx) {
			resp.State.RemoveResource(ctx)
			return
		}
	}
	if err == nil {
		err = m.decode(out)
	}
	if err != nil {
		resp.Diagnostics.AddError("Read project failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Organization.ValueString() != r.c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "This project belongs to another provider organization.")
		return
	}
	out, err := r.c.Do(ctx, "orgs", "PATCH", client.OrgPath(r.c.OrganizationID, "projects", m.ID.ValueString()), m.body())
	if err == nil {
		err = m.decode(out)
	}
	if err != nil {
		resp.Diagnostics.AddError("Update project failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Organization.ValueString() != r.c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "This project belongs to another provider organization.")
		return
	}
	_, err := r.c.Do(ctx, "orgs", "DELETE", client.OrgPath(r.c.OrganizationID, "projects", m.ID.ValueString()), nil)
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 && r.organizationVisible(ctx) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Delete project failed", err.Error())
	}
}
func (r *projectResource) organizationVisible(ctx context.Context) bool {
	_, err := r.c.Do(ctx, "orgs", "GET", "/v1/organizations/"+url.PathEscape(r.c.OrganizationID), nil)
	return err == nil
}
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] != r.c.OrganizationID || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid project import", "Use organization_id/project_id matching the configured organization.")
		return
	}
	m := projectModel{ID: types.StringValue(parts[1]), Organization: types.StringValue(parts[0]), Slug: types.StringNull(), Name: types.StringNull(), Folder: types.StringNull()}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
