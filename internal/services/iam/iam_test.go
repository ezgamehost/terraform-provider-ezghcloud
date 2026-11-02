package iam

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"testing"
)

func TestImportEscapedSSOPrincipal(t *testing.T) {
	parts, err := parseImport("org_acme/ssoUser/ezgh%3Aus-west-1%3Aorg_acme%3Adir_a%2Fusr_b/ReadOnlyAccess", 4)
	if err != nil || parts[2] != "ezgh:us-west-1:org_acme:dir_a/usr_b" {
		t.Fatalf("parts=%v err=%v", parts, err)
	}
	if _, err := parseImport("org_acme/extra", 4); err == nil {
		t.Fatal("accepted incomplete import")
	}
}

func TestWirePolicyStatementsAndNullOmission(t *testing.T) {
	ctx := context.Background()
	statementType := types.ObjectType{AttrTypes: map[string]attr.Type{"effect": types.StringType, "actions": types.SetType{ElemType: types.StringType}, "resources": types.SetType{ElemType: types.StringType}, "sid": types.StringType}}
	statement := types.ObjectValueMust(statementType.AttrTypes, map[string]attr.Value{"effect": types.StringValue("allow"), "actions": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("instances.get")}), "resources": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("*")}), "sid": types.StringNull()})
	wire, err := toWire(ctx, types.ListValueMust(statementType, []attr.Value{statement}))
	if err != nil {
		t.Fatal(err)
	}
	got := wire.([]any)[0].(map[string]any)
	if got["effect"] != "allow" {
		t.Fatalf("%v", got)
	}
	if _, ok := got["sid"]; ok {
		t.Fatalf("null sid included: %v", got)
	}
}

func TestRefreshPreservesOneTimeSecret(t *testing.T) {
	d := definitions()["api_key"]
	before := nullValues(d)
	before["token"] = types.StringValue("do-not-lose")
	after, err := mergeResponse(context.Background(), d, before, map[string]any{"id": "key-id", "name": "deploy", "prefix": "ezgh_abc"})
	if err != nil {
		t.Fatal(err)
	}
	if after["token"].(types.String).ValueString() != "do-not-lose" {
		t.Fatal("lost secret")
	}
}

func TestAllIAMResourceSchemasValidate(t *testing.T) {
	for _, newResource := range Resources() {
		r := newResource().(*iamResource)
		diagnostics := schemaFor(r.definition.name).ValidateImplementation(context.Background())
		if diagnostics.HasError() {
			t.Errorf("%s: %v", r.definition.name, diagnostics)
		}
	}
}

func TestImportedRequiredFieldsRecovered(t *testing.T) {
	d := definitions()["bot"]
	v := nullValues(d)
	got, err := mergeResponse(context.Background(), d, v, map[string]any{"id": "bot-uuid", "name": "deploy", "slug": "bot_deploy", "resourceName": "ezgh:bot_deploy"})
	if err != nil || stringValue(got, "name") != "deploy" {
		t.Fatalf("%v %v", got, err)
	}
}
