package iam

import (
	"context"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"strings"
)

type iamResource struct {
	definition definition
	client     *client.Client
}

func Resources() []func() resource.Resource {
	out := []func() resource.Resource{}
	for _, name := range []string{"bot", "group", "policy", "group_membership", "policy_attachment", "bot_trust", "oidc_provider", "bot_oidc_trust", "api_key", "invitation"} {
		n := name
		out = append(out, func() resource.Resource { return &iamResource{definition: definitions()[n]} })
	}
	return out
}
func (r *iamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.definition.name
}
func (r *iamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schemaFor(r.definition.name)
}
func (r *iamResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider configuration", "Expected EZGH Cloud API client.")
		return
	}
	r.client = c
}
func readRaw(ctx context.Context, d definition, raw tftypes.Value) (map[string]attr.Value, error) {
	var fields map[string]tftypes.Value
	if e := raw.As(&fields); e != nil {
		return nil, e
	}
	out := nullValues(d)
	for k, f := range d.fields {
		if rawValue, ok := fields[k]; ok {
			v, e := f.typ.ValueFromTerraform(ctx, rawValue)
			if e != nil {
				return nil, e
			}
			out[k] = v
		}
	}
	return out, nil
}
func objectValue(d definition, v map[string]attr.Value) types.Object {
	return types.ObjectValueMust(attributeTypes(d), v)
}
func (r *iamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	v, e := readRaw(ctx, r.definition, req.Plan.Raw)
	if e != nil {
		resp.Diagnostics.AddError("Invalid plan", e.Error())
		return
	}
	if r.definition.kind == "relation" {
		v, e = changeRelation(ctx, r.client, r.definition, v, http.MethodPost)
	} else {
		v, e = createEntity(ctx, r.client, r.definition, v)
	}
	if stringValue(v, "id") != "" {
		resp.Diagnostics.Append(resp.State.Set(ctx, objectValue(r.definition, v))...)
	}
	if e != nil {
		resp.Diagnostics.AddError("Unable to create IAM resource", e.Error())
	}
}
func (r *iamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	v, e := readRaw(ctx, r.definition, req.State.Raw)
	if e != nil {
		resp.Diagnostics.AddError("Invalid state", e.Error())
		return
	}
	if stringValue(v, "organization_id") != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Resource belongs to another organization; configure an aliased provider for that organization.")
		return
	}
	missing := false
	if r.definition.kind == "relation" {
		var exists bool
		exists, e = readRelation(ctx, r.client, r.definition, v)
		missing = !exists
	} else {
		v, missing, e = readEntity(ctx, r.client, r.definition, v)
	}
	if e != nil {
		resp.Diagnostics.AddError("Unable to read IAM resource", e.Error())
		return
	}
	if missing {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, objectValue(r.definition, v))...)
}
func (r *iamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	prior, priorErr := readRaw(ctx, r.definition, req.State.Raw)
	if priorErr != nil {
		resp.Diagnostics.AddError("Invalid state", priorErr.Error())
		return
	}
	if stringValue(prior, "organization_id") != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Resource belongs to another organization; configure an aliased provider for that organization.")
		return
	}
	v, e := readRaw(ctx, r.definition, req.Plan.Raw)
	if e != nil {
		resp.Diagnostics.AddError("Invalid plan", e.Error())
		return
	}
	if r.definition.kind == "relation" || r.definition.name == "api_key" || r.definition.name == "invitation" || r.definition.name == "bot_oidc_trust" {
		resp.Diagnostics.AddError("Replacement required", "This IAM resource's inputs are immutable; replace the resource to change them.")
		return
	}
	v, e = updateEntity(ctx, r.client, r.definition, v)
	if e != nil {
		resp.Diagnostics.AddError("Unable to update IAM resource", e.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, objectValue(r.definition, v))...)
}
func (r *iamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	v, e := readRaw(ctx, r.definition, req.State.Raw)
	if e != nil {
		resp.Diagnostics.AddError("Invalid state", e.Error())
		return
	}
	if stringValue(v, "organization_id") != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Resource belongs to another organization; configure an aliased provider for that organization.")
		return
	}
	if r.definition.kind == "relation" {
		_, e = changeRelation(ctx, r.client, r.definition, v, http.MethodDelete)
	} else {
		e = deleteEntity(ctx, r.client, r.definition, v)
	}
	if e != nil {
		resp.Diagnostics.AddError("Unable to delete IAM resource", e.Error())
	}
}
func (r *iamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	n := 2
	if r.definition.kind == "relation" {
		n = 4
	}
	if r.definition.name == "bot_oidc_trust" {
		n = 3
	}
	parts, e := parseImport(req.ID, n)
	if e != nil {
		resp.Diagnostics.AddError("Invalid import ID", e.Error())
		return
	}
	if parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	v := nullValues(r.definition)
	v["organization_id"] = types.StringValue(parts[0])
	v["id"] = types.StringValue(parts[len(parts)-1])
	if r.definition.kind == "relation" {
		v["principal_type"] = types.StringValue(parts[1])
		v["principal_id"] = types.StringValue(parts[2])
		key := "policy_id"
		if r.definition.name == "group_membership" {
			key = "group_id"
		}
		v[key] = types.StringValue(parts[3])
		v["id"] = types.StringValue(strings.Join(parts[1:], "/"))
	}
	if r.definition.name == "bot_trust" || r.definition.name == "bot_oidc_trust" {
		v["bot_id"] = types.StringValue(parts[1])
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, objectValue(r.definition, v))...)
}

var _ resource.Resource = &iamResource{}
var _ resource.ResourceWithImportState = &iamResource{}
var _ resource.ResourceWithConfigure = &iamResource{}
