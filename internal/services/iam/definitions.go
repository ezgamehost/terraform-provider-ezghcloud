package iam

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type field struct {
	typ                       attr.Type
	attribute                 schema.Attribute
	wire                      string
	preserve, output, replace bool
}
type definition struct {
	name, collection, kind string
	fields                 map[string]field
}

func str(w string, required, output, replace bool) field {
	a := schema.StringAttribute{Required: required, Computed: output, Optional: !required && !output, Description: "The " + w + " value."}
	if w == "description" {
		a.Computed = true
	}
	if output {
		a.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	}
	if replace {
		a.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	}
	return field{typ: types.StringType, attribute: a, wire: w, output: output, replace: replace}
}
func stringsField(w string, required, output, replace bool) field {
	a := schema.SetAttribute{ElementType: types.StringType, Required: required, Computed: output, Optional: !required && !output, Description: "The " + w + " values."}
	if w == "groupIds" || w == "policyIds" {
		a.Computed = true
	}
	if replace {
		a.PlanModifiers = []planmodifier.Set{setplanmodifier.RequiresReplace()}
	}
	return field{typ: types.SetType{ElemType: types.StringType}, attribute: a, wire: w, output: output, replace: replace}
}
func mapField(w string, replace bool) field {
	a := schema.MapAttribute{ElementType: types.StringType, Optional: true, Computed: true, Description: "Exact claim constraints."}
	if replace {
		a.PlanModifiers = []planmodifier.Map{mapplanmodifier.RequiresReplace()}
	}
	return field{typ: types.MapType{ElemType: types.StringType}, attribute: a, wire: w, replace: replace}
}
func integer(w string) field {
	return field{typ: types.Int64Type, attribute: schema.Int64Attribute{Optional: true, Description: "Requested lifetime in seconds (900 to 31536000).", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}}, wire: w, replace: true}
}
func nested(w string, nested map[string]schema.Attribute, required bool) field {
	a := schema.ListNestedAttribute{Required: required, Optional: !required, NestedObject: schema.NestedAttributeObject{Attributes: nested}, Description: "Ordered " + w + " entries."}
	return field{typ: a.GetType(), attribute: a, wire: w}
}
func definitions() map[string]definition {
	base := func(name, collection string, fs map[string]field) definition {
		fs["id"] = str("id", false, true, false)
		fs["organization_id"] = str("", false, true, false)
		return definition{name: name, collection: collection, kind: "entity", fields: fs}
	}
	ds := map[string]definition{}
	ds["bot"] = base("bot", "bots", map[string]field{"name": str("name", true, false, false), "slug": str("slug", false, true, false), "resource_name": str("resourceName", false, true, false)})
	ds["group"] = base("group", "groups", map[string]field{"name": str("name", true, false, false), "description": str("description", false, false, false), "resource_name": str("resourceName", false, true, false)})
	statements := nested("statements", map[string]schema.Attribute{"sid": schema.StringAttribute{Optional: true}, "effect": schema.StringAttribute{Required: true}, "actions": schema.SetAttribute{Required: true, ElementType: types.StringType}, "resources": schema.SetAttribute{Required: true, ElementType: types.StringType}}, true)
	ds["policy"] = base("policy", "policies", map[string]field{"name": str("name", true, false, false), "description": str("description", false, false, false), "statements": statements, "resource_name": str("resourceName", false, true, false)})
	ds["oidc_provider"] = base("oidc_provider", "oidc-providers", map[string]field{"name": str("name", true, false, false), "issuer": str("issuer", true, false, true), "audiences": stringsField("audiences", true, false, false), "resource_name": str("resourceName", false, true, false)})
	provider := ds["oidc_provider"]
	jwks := str("jwks", false, false, false)
	jwks.preserve = true
	provider.fields["jwks_json"] = jwks
	provider.fields["key_source"] = str("keySource", false, true, false)
	ds["oidc_provider"] = provider
	ds["api_key"] = base("api_key", "api-keys", map[string]field{"name": str("name", true, false, true), "bot_id": str("botId", false, false, true), "expires_in_seconds": integer("expiresInSeconds"), "token": str("token", false, true, false), "prefix": str("prefix", false, true, false), "expires_at": str("expiresAt", false, true, false)})
	d := ds["api_key"]
	f := d.fields["token"]
	f.preserve = true
	a := f.attribute.(schema.StringAttribute)
	a.Sensitive = true
	a.Description = "One-time API key token. Stored in Terraform state; unavailable after import. Creation requires a user OAuth bearer token, not an API key."
	f.attribute = a
	d.fields["token"] = f
	ds["api_key"] = d
	ds["invitation"] = base("invitation", "invitations", map[string]field{"email": str("email", true, false, true), "group_ids": stringsField("groupIds", false, false, true), "policy_ids": stringsField("policyIds", false, false, true), "expires_in_days": integer("expiresInDays"), "status": str("status", false, true, false), "url": str("url", false, true, false), "expires_at": str("expiresAt", false, true, false)})
	ds["bot_oidc_trust"] = base("bot_oidc_trust", "bots", map[string]field{"bot_id": str("", true, false, true), "provider_id": str("providerId", true, false, true), "subject": str("subject", true, false, true), "claims": mapField("claims", true)})
	ds["bot_trust"] = base("bot_trust", "bots", map[string]field{"bot_id": str("", true, false, true), "trust": nested("trust", map[string]schema.Attribute{"principal_type": schema.StringAttribute{Required: true}, "resource_name": schema.StringAttribute{Required: true}}, true)})
	for _, name := range []string{"group_membership", "policy_attachment"} {
		fs := map[string]field{"principal_type": str("", true, false, true), "principal_id": str("", true, false, true)}
		if name == "group_membership" {
			fs["group_id"] = str("", true, false, true)
		} else {
			fs["policy_id"] = str("", true, false, true)
		}
		d := base(name, "", fs)
		d.kind = "relation"
		ds[name] = d
	}
	return ds
}
func attributeTypes(d definition) map[string]attr.Type {
	out := map[string]attr.Type{}
	for k, f := range d.fields {
		out[k] = f.typ
	}
	return out
}
func schemaAttributes(d definition) map[string]schema.Attribute {
	out := map[string]schema.Attribute{}
	for k, f := range d.fields {
		out[k] = f.attribute
	}
	return out
}
func schemaFor(name string) schema.Schema {
	return schema.Schema{Description: "Manage an EZGH Cloud " + name + ".", Attributes: schemaAttributes(definitions()[name])}
}
