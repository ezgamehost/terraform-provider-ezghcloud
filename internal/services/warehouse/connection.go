package warehouse

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// topicPrefix is the start of Kafka topic names warehouse-api takes.
var topicPrefix = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

func connectionNameProblem(name string) string {
	switch {
	case !userName.MatchString(name):
		return "Use 1–32 lowercase letters, digits and underscores, starting with a letter."
	case strings.HasPrefix(name, "ezgh_"):
		return name + " is reserved."
	}
	return ""
}

// connectionTarget is each type's target ID prefix.
var connectionTarget = map[string]string{"bucket": "bkt_", "kafka": "hqc_", "database": "dbi_"}

// connectionModel is a warehouse's connection, by name; the Terraform ID is <whs_>/<name>. There is
// no update route: every configured attribute replaces it.
type connectionModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	WarehouseID    types.String `tfsdk:"warehouse_id"`
	Name           types.String `tfsdk:"name"`
	Type           types.String `tfsdk:"type"`
	TargetID       types.String `tfsdk:"target_id"`
	Access         types.String `tfsdk:"access"`
	TopicPrefix    types.String `tfsdk:"topic_prefix"`
	Database       types.String `tfsdk:"database"`
	SecretID       types.String `tfsdk:"secret_id"`
	Status         types.String `tfsdk:"status"`
	FailureCode    types.String `tfsdk:"failure_code"`
	FailureMessage types.String `tfsdk:"failure_message"`
}

type connectionView struct {
	Name, Type, Target, Status    string
	Access, TopicPrefix, Database *string
	Secret                        *string
	Failure                       *struct{ Code, Message string }
}

type connectionResource struct{ client *client.Client }

func (*connectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_warehouse_connection"
}
func (*connectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A warehouse's connection to an Object Storage bucket, a Hosted Queues Kafka cluster or an EZGH Databases database, which SQL names as name: s3(name, …), ENGINE = Kafka(name), postgresql(name, …) or mysql(name, …). Creating it needs the target's own permissions: it adds the warehouse to the bucket's grants, or creates a read-only Kafka or database user whose password is kept in a Secret. A warehouse has one connection to each bucket. Kafka clusters and databases must be in the warehouse's VPC. Destroying it deletes the credential it created. A connection whose target was deleted is broken until destroyed.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "warehouse_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the warehouse. Changing it replaces the connection.", PlanModifiers: replace},
		"warehouse_id":    schema.StringAttribute{Required: true, Description: "Warehouse (whs_) it belongs to. Changing it replaces the connection.", PlanModifiers: replace},
		"name": schema.StringAttribute{Required: true, Description: "The name SQL uses: a lowercase letter, then up to 31 lowercase letters, digits and _. Names starting ezgh_ are reserved. Changing it replaces the connection.", PlanModifiers: replace,
			Validators: []validator.String{nameValidator{"connection name", connectionNameProblem}}},
		"type":            schema.StringAttribute{Required: true, Description: "bucket, kafka or database. Changing it replaces the connection.", PlanModifiers: replace},
		"target_id":       schema.StringAttribute{Required: true, Description: "What it reaches: a bucket (bkt_), a Kafka cluster (hqc_) or a database (dbi_). Changing it replaces the connection.", PlanModifiers: replace},
		"access":          schema.StringAttribute{Optional: true, Description: "A bucket's access: read or readWrite (INSERT INTO FUNCTION s3(…) too). Required for a bucket, refused otherwise. Changing it replaces the connection.", PlanModifiers: replace},
		"topic_prefix":    schema.StringAttribute{Optional: true, Description: "Kafka only: consume only topics starting with it, up to 200 of A-Z, a-z, 0-9, ., _ and -, in consumer groups whose names start with the username in secret_id. Every topic when unset. Changing it replaces the connection.", PlanModifiers: replace},
		"database":        schema.StringAttribute{Optional: true, Description: "A database connection's logical database. Required for a database, refused otherwise. Changing it replaces the connection.", PlanModifiers: replace},
		"secret_id":       schema.StringAttribute{Computed: true, Description: "The Secret (sec_) holding a Kafka or database connection's user and password; null for a bucket.", PlanModifiers: keepString},
		"status":          schema.StringAttribute{Computed: true, Description: "creating, available, broken (its target is gone), deleting or failed."},
		"failure_code":    schema.StringAttribute{Computed: true, Description: "Why the connection failed or broke, as a code; null when it didn't."},
		"failure_message": schema.StringAttribute{Computed: true, Description: "Why the connection failed or broke; null when it didn't."},
	}}
}
func (r *connectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ValidateConfig refuses at plan what the API refuses: an unknown type, a target of another type,
// and each type's fields on another.
func (r *connectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m connectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !known(m.Type) {
		return
	}
	typ := m.Type.ValueString()
	prefix, ok := connectionTarget[typ]
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("type"), "Invalid connection type", "type is bucket, kafka or database.")
		return
	}
	if known(m.TargetID) && (!strings.HasPrefix(m.TargetID.ValueString(), prefix) || !prefixedID.MatchString(m.TargetID.ValueString())) {
		resp.Diagnostics.AddAttributeError(path.Root("target_id"), "Invalid target", "A "+typ+" connection's target is a "+prefix+"<12-character ID>.")
	}
	field := func(name string, v types.String, wanted bool) {
		switch {
		case wanted && v.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(name), "Missing "+name, "A "+typ+" connection needs "+name+".")
		case !wanted && !v.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(name), name+" doesn't apply", name+" isn't for a "+typ+" connection.")
		}
	}
	field("access", m.Access, typ == "bucket")
	field("database", m.Database, typ == "database")
	if typ != "kafka" {
		field("topic_prefix", m.TopicPrefix, false)
	}
	if known(m.Access) && m.Access.ValueString() != "read" && m.Access.ValueString() != "readWrite" {
		resp.Diagnostics.AddAttributeError(path.Root("access"), "Invalid access", "access is read or readWrite.")
	}
	if known(m.Database) && (m.Database.ValueString() == "" || len(m.Database.ValueString()) > 64) {
		resp.Diagnostics.AddAttributeError(path.Root("database"), "Invalid database", "A database name is 1 to 64 characters.")
	}
	if known(m.TopicPrefix) && !topicPrefix.MatchString(m.TopicPrefix.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("topic_prefix"), "Invalid topic_prefix", "Up to 200 of A-Z, a-z, 0-9, ., _ and -.")
	}
}

func (m *connectionModel) path(c *client.Client, extra ...string) string {
	return client.ProjectPath(c.OrganizationID, m.ProjectID.ValueString(), "warehouses", append([]string{m.WarehouseID.ValueString(), "connections"}, extra...)...)
}

func (r *connectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m connectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.WarehouseID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	body := map[string]any{"name": m.Name.ValueString(), "type": m.Type.ValueString(), "target": m.TargetID.ValueString()}
	setString(body, "access", m.Access)
	setString(body, "topicPrefix", m.TopicPrefix)
	setString(body, "database", m.Database)
	// Saved only once the API took it: a refused create leaves nothing to destroy.
	saved := func(*client.Response) bool {
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	if err := send(ctx, r.client, "POST", m.path(r.client), body, saved); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	v, err := readConnection(ctx, r.client, &m)
	if err != nil {
		resp.Diagnostics.AddWarning("Couldn't read "+m.Name.ValueString(), "It was created, but reading it back failed: "+err.Error()+". The next refresh reads it.")
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return
	}
	m.fill(v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Read keeps a broken connection (its target is gone) in state, so a plan shows it; one being
// deleted is gone.
func (r *connectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m connectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readConnection(ctx, r.client, &m)
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

// Update has nothing to change: every configured attribute replaces.
func (r *connectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m connectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete deletes the connection and the credential it created; one already being deleted is waited out.
func (r *connectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m connectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	p := m.path(r.client, m.Name.ValueString())
	err := send(ctx, r.client, "DELETE", p, nil, nil)
	if deleteInProgress(err) {
		err = waitDeleting(ctx, r.client, p, err)
	}
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes organization_id/project_id/whs_…/name.
func (r *connectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

func readConnection(ctx context.Context, c *client.Client, m *connectionModel) (connectionView, error) {
	var v connectionView
	out, err := c.Do(ctx, service, "GET", m.path(c, m.Name.ValueString()), nil)
	if err == nil {
		err = json.Unmarshal(out.Body, &v)
	}
	if err == nil && v.Name == "" {
		err = errors.New("the connection came back without a name")
	}
	return v, err
}

func (m *connectionModel) fill(v connectionView, org string) {
	m.OrganizationID, m.ID = types.StringValue(org), types.StringValue(m.WarehouseID.ValueString()+"/"+v.Name)
	m.Name, m.Type, m.TargetID, m.Status = types.StringValue(v.Name), types.StringValue(v.Type), types.StringValue(v.Target), types.StringValue(v.Status)
	m.Access, m.TopicPrefix, m.Database, m.SecretID = types.StringPointerValue(v.Access), types.StringPointerValue(v.TopicPrefix), types.StringPointerValue(v.Database), types.StringPointerValue(v.Secret)
	m.FailureCode, m.FailureMessage = types.StringNull(), types.StringNull()
	if v.Failure != nil {
		m.FailureCode, m.FailureMessage = types.StringValue(v.Failure.Code), types.StringValue(v.Failure.Message)
	}
}
