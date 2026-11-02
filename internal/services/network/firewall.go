package network

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type firewallResource struct{ client *client.Client }
type firewallModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Tags           types.Map    `tfsdk:"tags"`
	ResourceName   types.String `tfsdk:"resource_name"`
	Status         types.String `tfsdk:"status"`
	Rules          types.Set    `tfsdk:"rules"`
	ETag           types.String `tfsdk:"etag"`
}
type firewallWire struct {
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Tags         map[string]string `json:"tags"`
	ResourceName string            `json:"resourceName"`
	Status       string            `json:"status"`
	Rules        []ruleWire        `json:"rules"`
}

func (m *firewallModel) fromWire(ctx context.Context, v firewallWire, etag string) {
	m.ID = types.StringValue(v.Slug)
	m.Name = types.StringValue(v.Name)
	m.Description = types.StringValue(v.Description)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.ResourceName = types.StringValue(v.ResourceName)
	m.Status = types.StringValue(v.Status)
	m.ETag = types.StringValue(etag)
	m.Rules, _ = types.SetValueFrom(ctx, types.ObjectType{AttrTypes: ruleTypes()}, decodeRules(v.Rules))
}
func (r *firewallResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}
func (r *firewallResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := baseAttributes()
	a["description"] = schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Firewall description."}
	a["etag"] = schema.StringAttribute{Computed: true, Description: "Strong service version used for conditional rule replacement."}
	a["rules"] = schema.SetNestedAttribute{Optional: true, Computed: true, Default: setdefault.StaticValue(types.SetValueMust(types.ObjectType{AttrTypes: ruleTypes()}, nil)), NestedObject: schema.NestedAttributeObject{Attributes: ruleAttributes()}, Description: "Complete owned rule collection. Omitted or [] means no rules; no automatic outbound rules are added."}
	resp.Schema = schema.Schema{Description: "Manage a firewall and its complete set of rules. Do not manage the same firewall's rules with another resource or tool.", Attributes: a}
}
func (r *firewallResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", fmt.Sprintf("Expected API client, got %T", req.ProviderData))
	}
}
func (r *firewallResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m firewallModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Rules.IsNull() || m.Rules.IsUnknown() {
		return
	}
	var rules []ruleModel
	resp.Diagnostics.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := encodeRules(rules); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Invalid firewall rules", err.Error())
	}
	if len(rules) > 200 {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Too many firewall rules", "At most 200 rules are supported.")
	}
}
func (r *firewallResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.OrganizationID = types.StringValue(r.client.OrganizationID)
	var tags map[string]string
	resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	var rules []ruleModel
	resp.Diagnostics.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wire, err := encodeRules(rules)
	if err != nil {
		resp.Diagnostics.AddError("Invalid firewall rules", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "firewall", "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "firewalls"), map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueString(), "tags": tags, "rules": wire})
	if err != nil {
		resp.Diagnostics.AddError("Create firewall", err.Error())
		return
	}
	var v firewallWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode firewall", err.Error())
		return
	}
	m.fromWire(ctx, v, response.Header.Get("ETag"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	response, err := r.client.Do(ctx, "firewall", "GET", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "firewalls", m.ID.ValueString()), nil)
	if err != nil {
		gone, e := readGone(ctx, r.client, m.ProjectID.ValueString(), err)
		if gone {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read firewall", e.Error())
		return
	}
	var v firewallWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode firewall", err.Error())
		return
	}
	m.fromWire(ctx, v, response.Header.Get("ETag"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *firewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	p := client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "firewalls", m.ID.ValueString())
	response, err := r.client.Do(ctx, "firewall", "GET", p, nil)
	if err != nil {
		resp.Diagnostics.AddError("Read firewall version", err.Error())
		return
	}
	etag := response.Header.Get("ETag")
	if etag == "" || (!prior.ETag.IsNull() && prior.ETag.ValueString() != etag) {
		resp.Diagnostics.AddError("Firewall changed since refresh", "Refresh the firewall and create a new plan before replacing its rules.")
		return
	}
	if !m.Name.Equal(prior.Name) || !m.Description.Equal(prior.Description) || !m.Tags.Equal(prior.Tags) {
		var tags map[string]string
		resp.Diagnostics.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		response, err = r.client.Do(ctx, "firewall", "PATCH", p, map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueString(), "tags": tags})
		if err != nil {
			resp.Diagnostics.AddError("Update firewall", err.Error())
			return
		}
		etag = response.Header.Get("ETag")
	}
	if !m.Rules.Equal(prior.Rules) {
		if etag == "" {
			resp.Diagnostics.AddError("Missing firewall version", "Service did not return an ETag; refusing unconditional rule replacement.")
			return
		}
		var rules []ruleModel
		resp.Diagnostics.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		wire, encodeErr := encodeRules(rules)
		if encodeErr != nil {
			resp.Diagnostics.AddError("Invalid firewall rules", encodeErr.Error())
			return
		}
		_, err = r.client.DoWithHeaders(ctx, "firewall", "PUT", p+"/rules", map[string]any{"rules": wire}, http.Header{"If-Match": []string{etag}})
		if err != nil {
			resp.Diagnostics.AddError("Replace firewall rules", err.Error())
			return
		}
	}
	response, err = r.client.Do(ctx, "firewall", "GET", p, nil)
	if err != nil {
		resp.Diagnostics.AddError("Refresh firewall", err.Error())
		return
	}
	var v firewallWire
	if err = response.Decode(&v); err != nil {
		resp.Diagnostics.AddError("Decode firewall", err.Error())
		return
	}
	m.fromWire(ctx, v, response.Header.Get("ETag"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *firewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := checkOrg(r.client, m.OrganizationID); err != nil {
		resp.Diagnostics.AddError("Organization mismatch", err.Error())
		return
	}
	if err := deleteResource(ctx, r.client, "firewall", m.ProjectID.ValueString(), "firewalls", m.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete firewall", err.Error())
	}
}
func (r *firewallResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, project, id, err := parseImport(req.ID, "fwl_")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if r.client != nil && org != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the provider organization.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), org)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
}
