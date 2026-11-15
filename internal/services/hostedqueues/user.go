package hostedqueues

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// userModel is a cluster user, addressed by name under its cluster; the Terraform ID is <hqc_>/<name>.
// No attribute holds its password: it's in the Secret.
type userModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	ClusterID      types.String `tfsdk:"cluster_id"`
	Name           types.String `tfsdk:"name"`
	Preset         types.String `tfsdk:"preset"`
	Topics         types.List   `tfsdk:"topics"`
	Groups         types.List   `tfsdk:"groups"`
	Vhosts         types.List   `tfsdk:"vhosts"`
	SecretID       types.String `tfsdk:"secret_id"`
	Status         types.String `tfsdk:"status"`
	FailureCode    types.String `tfsdk:"failure_code"`
	FailureMessage types.String `tfsdk:"failure_message"`
}
type topicGrant struct {
	Topic  types.String `tfsdk:"topic"`
	Access types.String `tfsdk:"access"`
	Create types.Bool   `tfsdk:"create"`
}
type vhostGrant struct {
	Vhost     types.String `tfsdk:"vhost"`
	Preset    types.String `tfsdk:"preset"`
	Configure types.String `tfsdk:"configure"`
	Write     types.String `tfsdk:"write"`
	Read      types.String `tfsdk:"read"`
}

var (
	topicGrantType = types.ObjectType{AttrTypes: map[string]attr.Type{"topic": types.StringType, "access": types.StringType, "create": types.BoolType}}
	vhostGrantType = types.ObjectType{AttrTypes: map[string]attr.Type{"vhost": types.StringType, "preset": types.StringType, "configure": types.StringType, "write": types.StringType, "read": types.StringType}}
)

type userView struct {
	Name, Status string
	Secret       *string
	Permissions  struct {
		Preset *string
		Topics []struct {
			Topic, Access string
			Create        bool
		}
		Groups []struct{ Group string }
		Vhosts []struct{ Vhost, Preset, Configure, Write, Read string }
	}
	Failure *struct{ Code, Message string }
}

type userResource struct{ client *client.Client }

func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hosted_queue_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optional := func(d string) schema.StringAttribute { return schema.StringAttribute{Optional: true, Description: d} }
	resp.Schema = schema.Schema{Description: "A user of a Hosted Queues cluster. Its password is generated and kept in a Secret in the cluster's project; Terraform state never holds it. Permission changes apply without restarting the cluster. Destroying it deletes the user and its Secret. The user app comes with the cluster: import it to manage it.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "cluster_id/name.", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the cluster. Changing it replaces the user.", PlanModifiers: replace},
		"cluster_id":      schema.StringAttribute{Required: true, Description: "Cluster (hqc_) it belongs to. Changing it replaces the user.", PlanModifiers: replace},
		"name":            schema.StringAttribute{Required: true, Description: "Username: a lowercase letter, then up to 31 lowercase letters, digits, _ and -. Changing it replaces the user (a new password in a new Secret).", PlanModifiers: replace},
		"preset":          schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "Kafka only: admin (every topic and group, and topic configs), readWrite (produce and consume every topic, create topics), readOnly (consume every topic) or custom (topics and groups). readWrite when left out at create; removing it later keeps the current preset. Null on RabbitMQ. Changes in place."},
		"topics": schema.ListNestedAttribute{Optional: true, Description: "Kafka, with preset custom: topic grants. Changes in place.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"topic":  schema.StringAttribute{Required: true, Description: "A topic's name, a prefix of one ending in *, or * for every topic."},
			"access": schema.StringAttribute{Required: true, Description: "produce, consume or produceConsume."},
			"create": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Whether the user may create the topics it names."},
		}}},
		"groups": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Kafka, with preset custom: the consumer groups it may use, each a name or a prefix ending in *. Changes in place."},
		"vhosts": schema.ListNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}, Description: "RabbitMQ only: the vhosts it may use. readWrite on the default vhost / when left out at create; removing it later keeps the current list. Null on Kafka. Changes in place.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"vhost":     schema.StringAttribute{Required: true, Description: "The vhost's name; / is the default vhost."},
			"preset":    schema.StringAttribute{Required: true, Description: "readWrite, readOnly or custom."},
			"configure": optional("With preset custom: RabbitMQ's configure regular expression."),
			"write":     optional("With preset custom: RabbitMQ's write regular expression."),
			"read":      optional("With preset custom: RabbitMQ's read regular expression."),
		}}},
		"secret_id":       schema.StringAttribute{Computed: true, Description: "The Secret (sec_) holding the user's username and password and how to connect: host, port, plaintextPort and engine; on Kafka bootstrapServers, saslMechanism and securityProtocol; on RabbitMQ vhost and an amqps:// uri.", PlanModifiers: keepString},
		"status":          schema.StringAttribute{Computed: true, Description: "What the user is doing: creating, available, modifying, rotating, deleting or failed."},
		"failure_code":    schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed, as a code; null when it didn't."},
		"failure_message": schema.StringAttribute{Computed: true, Description: "Why the user's last operation failed; null when it didn't."},
	}}
}
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ModifyPlan marks secret_id unknown when the vhosts change: losing the Secret's vhost writes a new
// password, into a new Secret when the old one is gone.
func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state types.List
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("vhosts"), &plan)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("vhosts"), &state)...)
	if !resp.Diagnostics.HasError() && !plan.Equal(state) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("secret_id"), types.StringUnknown())...)
	}
}

func (m *userModel) path(c *client.Client, extra ...string) string {
	return client.ProjectPath(c.OrganizationID, m.ProjectID.ValueString(), "clusters", append([]string{m.ClusterID.ValueString(), "users", m.Name.ValueString()}, extra...)...)
}

// permissions is the request's permissions from what's configured; empty when nothing is, which a
// create leaves to the API.
func (m *userModel) permissions(ctx context.Context) (map[string]any, diag.Diagnostics) {
	var d diag.Diagnostics
	p := map[string]any{}
	setString(p, "preset", m.Preset)
	if known(m.Topics) && len(m.Topics.Elements()) > 0 {
		var grants []topicGrant
		d.Append(m.Topics.ElementsAs(ctx, &grants, false)...)
		list := []map[string]any{}
		for _, g := range grants {
			list = append(list, map[string]any{"topic": g.Topic.ValueString(), "access": g.Access.ValueString(), "create": g.Create.ValueBool()})
		}
		p["topics"] = list
	}
	if known(m.Groups) && len(m.Groups.Elements()) > 0 {
		var groups []string
		d.Append(m.Groups.ElementsAs(ctx, &groups, false)...)
		list := []map[string]any{}
		for _, g := range groups {
			list = append(list, map[string]any{"group": g})
		}
		p["groups"] = list
	}
	if known(m.Vhosts) && len(m.Vhosts.Elements()) > 0 {
		var grants []vhostGrant
		d.Append(m.Vhosts.ElementsAs(ctx, &grants, false)...)
		list := []map[string]any{}
		for _, g := range grants {
			v := map[string]any{"vhost": g.Vhost.ValueString(), "preset": g.Preset.ValueString()}
			setString(v, "configure", g.Configure)
			setString(v, "write", g.Write)
			setString(v, "read", g.Read)
			list = append(list, v)
		}
		p["vhosts"] = list
	}
	return p, d
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(m.ClusterID.ValueString()+"/"+m.Name.ValueString()), types.StringValue(r.client.OrganizationID), types.StringValue("creating")
	body := map[string]any{"name": m.Name.ValueString()}
	p, d := m.permissions(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(p) > 0 {
		body["permissions"] = p
	}
	// Saved only once the API took it: a refused create leaves nothing to destroy.
	saved := func(*client.Response) bool {
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	collection := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "clusters", m.ClusterID.ValueString(), "users")
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

// Update replaces the permissions in place, so the user and its password stay.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if !m.Preset.Equal(old.Preset) || !m.Topics.Equal(old.Topics) || !m.Groups.Equal(old.Groups) || !m.Vhosts.Equal(old.Vhosts) {
		p, d := m.permissions(ctx)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := send(ctx, r.client, "PATCH", m.path(r.client), map[string]any{"permissions": p}, nil); err != nil {
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

// ImportState takes organization_id/project_id/hqc_…/name.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], "hqc_") || !prefixedID.MatchString(parts[2]) || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/hqc_<12-character ID>/name.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "cluster_id", "name"} {
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

// fill reads the user into m. A vhost configured as "default" stays so: the API shows it as /.
func (m *userModel) fill(ctx context.Context, v userView, org string) diag.Diagnostics {
	var d diag.Diagnostics
	var before []vhostGrant
	if known(m.Vhosts) {
		d.Append(m.Vhosts.ElementsAs(ctx, &before, false)...)
	}
	m.ID = types.StringValue(m.ClusterID.ValueString() + "/" + m.Name.ValueString())
	m.OrganizationID = types.StringValue(org)
	p := v.Permissions
	m.Preset = types.StringPointerValue(p.Preset)
	empty := func(prior types.List, null types.List) types.List {
		if known(prior) && len(prior.Elements()) == 0 {
			return prior // configured as [], which the API shows as none
		}
		return null
	}
	m.Topics, m.Groups, m.Vhosts = empty(m.Topics, types.ListNull(topicGrantType)), empty(m.Groups, types.ListNull(types.StringType)), types.ListNull(vhostGrantType)
	if len(p.Topics) > 0 {
		var list []attr.Value
		for _, t := range p.Topics {
			list = append(list, types.ObjectValueMust(topicGrantType.AttrTypes, map[string]attr.Value{"topic": types.StringValue(t.Topic), "access": types.StringValue(t.Access), "create": types.BoolValue(t.Create)}))
		}
		m.Topics = types.ListValueMust(topicGrantType, list)
	}
	if len(p.Groups) > 0 {
		var groups []string
		for _, g := range p.Groups {
			groups = append(groups, g.Group)
		}
		m.Groups = stringList(ctx, groups)
	}
	if len(p.Vhosts) > 0 {
		var list []attr.Value
		for i, g := range p.Vhosts {
			name := g.Vhost
			if name == "/" && i < len(before) && before[i].Vhost.ValueString() == "default" {
				name = "default"
			}
			text := func(s string) types.String {
				if s == "" && g.Preset != "custom" { // "" is a custom grant's "nothing"
					return types.StringNull()
				}
				return types.StringValue(s)
			}
			list = append(list, types.ObjectValueMust(vhostGrantType.AttrTypes, map[string]attr.Value{"vhost": types.StringValue(name), "preset": types.StringValue(g.Preset),
				"configure": text(g.Configure), "write": text(g.Write), "read": text(g.Read)}))
		}
		m.Vhosts = types.ListValueMust(vhostGrantType, list)
	}
	m.SecretID = types.StringPointerValue(v.Secret)
	m.Status = types.StringValue(v.Status)
	m.FailureCode, m.FailureMessage = types.StringNull(), types.StringNull()
	if v.Failure != nil {
		m.FailureCode, m.FailureMessage = types.StringValue(v.Failure.Code), types.StringValue(v.Failure.Message)
	}
	return d
}
