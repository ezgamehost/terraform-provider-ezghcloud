package warehouse

import (
	"context"
	"errors"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// databaseModel is one database of a warehouse, by its exact name; the Terraform ID is
// <whs_>/<name>. Destroying this resource is the only thing in the provider that deletes a database,
// and it deletes only this one.
type databaseModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	WarehouseID    types.String `tfsdk:"warehouse_id"`
	Name           types.String `tfsdk:"name"`
}

type databaseResource struct{ client *client.Client }

func (*databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_warehouse_database"
}
func (*databaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A database of a warehouse, on every shard and replica: tables created in it with SQL reach every node. Tables are your SQL's, never Terraform's. Destroying this resource deletes this database and every table in it, and is refused while the warehouse's deletion_protection is on. A database that already exists isn't adopted: import it.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "warehouse_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the warehouse. Changing it replaces the database.", PlanModifiers: replace},
		"warehouse_id":    schema.StringAttribute{Required: true, Description: "Warehouse (whs_) it belongs to. Changing it replaces the database.", PlanModifiers: replace},
		"name": schema.StringAttribute{Required: true, Description: "The database's exact name: a letter or _, then up to 63 letters, digits and _. system, information_schema, INFORMATION_SCHEMA, default and names starting ezgh_ are reserved. Changing it replaces the database.", PlanModifiers: replace,
			Validators: []validator.String{nameValidator{"database name", databaseNameProblem}}},
	}}
}
func (r *databaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (m *databaseModel) collection(c *client.Client, extra ...string) string {
	return client.ProjectPath(c.OrganizationID, m.ProjectID.ValueString(), "warehouses", append([]string{m.WarehouseID.ValueString(), "databases"}, extra...)...)
}

func alreadyExists(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.StatusCode == 409 && e.Code == "already_exists"
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID = types.StringValue(m.WarehouseID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID)
	saved := func(*client.Response) bool {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return !resp.Diagnostics.HasError()
	}
	err := send(ctx, r.client, "POST", m.collection(r.client), map[string]any{"name": m.Name.ValueString()}, saved)
	if alreadyExists(err) {
		resp.Diagnostics.AddError("Database already exists", "Warehouse "+m.WarehouseID.ValueString()+" already has the database "+m.Name.ValueString()+". Terraform doesn't adopt it: run terraform import on "+r.client.OrganizationID+"/"+m.ProjectID.ValueString()+"/"+m.ID.ValueString()+" to manage it.")
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
	}
}

// Read checks the warehouse is still there.
// shortcut: warehouse-api has no GET of one database until C4; then read it, and drop the resource
// when the database is gone.
func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if _, err := readWarehouse(ctx, r.client, m.ProjectID.ValueString(), m.WarehouseID.ValueString()); err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
	}
}

// Update has nothing to change: every attribute replaces.
func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete deletes this database only, naming it in the body as the API asks.
func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if err := send(ctx, r.client, "DELETE", m.collection(r.client, m.Name.ValueString()), map[string]any{"name": m.Name.ValueString()}, nil); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes organization_id/project_id/whs_…/name.
func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
