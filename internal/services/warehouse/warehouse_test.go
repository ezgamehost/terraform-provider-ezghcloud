package warehouse

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func schemaOf(r resource.Resource) schema.Schema {
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	return sr.Schema
}

// replaces is whether a change to the string attribute replaces the resource.
func replaces(t *testing.T, s schema.Schema, name string) bool {
	t.Helper()
	a, ok := s.Attributes[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s isn't a string attribute", name)
	}
	for _, m := range a.PlanModifiers {
		if strings.Contains(m.Description(context.Background()), "destroy and recreate") {
			return true
		}
	}
	return false
}

func TestSchemasReplaceOnlyWhatMoves(t *testing.T) {
	w := schemaOf(&warehouseResource{})
	for _, name := range []string{"project_id", "subnet_id", "storage_kind", "from_backup"} {
		if !replaces(t, w, name) {
			t.Errorf("warehouse %s doesn't force replacement", name)
		}
	}
	for _, name := range []string{"name", "engine_version", "keeper", "instance_type"} {
		if replaces(t, w, name) {
			t.Errorf("warehouse %s forces replacement", name)
		}
	}
	u := schemaOf(&userResource{})
	for _, name := range []string{"project_id", "warehouse_id", "name"} {
		if !replaces(t, u, name) {
			t.Errorf("user %s doesn't force replacement", name)
		}
	}
	for _, name := range []string{"preset", "settings_profile"} {
		if replaces(t, u, name) {
			t.Errorf("user %s forces replacement", name)
		}
	}
	d := schemaOf(&databaseResource{})
	for _, name := range []string{"project_id", "warehouse_id", "name"} {
		if !replaces(t, d, name) {
			t.Errorf("database %s doesn't force replacement", name)
		}
	}
	// No update route: everything configured replaces a connection.
	c := schemaOf(&connectionResource{})
	for _, name := range []string{"project_id", "warehouse_id", "name", "type", "target_id", "access", "topic_prefix", "database"} {
		if !replaces(t, c, name) {
			t.Errorf("connection %s doesn't force replacement", name)
		}
	}
}

func TestUserWritesNoPasswordToState(t *testing.T) {
	for _, s := range []schema.Schema{schemaOf(&userResource{}), schemaOf(&warehouseResource{}), schemaOf(&connectionResource{})} {
		for name, a := range s.Attributes {
			if strings.Contains(name, "password") {
				t.Errorf("%s holds a password", name)
			}
			if a.IsSensitive() {
				t.Errorf("%s is sensitive: nothing should need to be", name)
			}
		}
	}
	if a := schemaOf(&userResource{}).Attributes["secret_id"]; !a.IsComputed() || a.IsOptional() || a.IsRequired() {
		t.Error("secret_id isn't computed only")
	}
}

func validate(t *testing.T, s schema.Schema, attr, value string) bool {
	t.Helper()
	var resp validator.StringResponse
	for _, v := range s.Attributes[attr].(schema.StringAttribute).Validators {
		v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root(attr), ConfigValue: types.StringValue(value)}, &resp)
	}
	return !resp.Diagnostics.HasError()
}

// The names warehouse-api takes, refused at plan at the regex's edges.
func TestNameValidatorsMatchTheAPI(t *testing.T) {
	d := schemaOf(&databaseResource{})
	for _, ok := range []string{"My_DB", "_x", strings.Repeat("a", 64)} {
		if !validate(t, d, "name", ok) {
			t.Errorf("database %q refused", ok)
		}
	}
	for _, bad := range []string{"1db", "a.b", "a`b", "über", "system", "INFORMATION_SCHEMA", "information_schema", "default", "ezgh_x", strings.Repeat("a", 65)} {
		if validate(t, d, "name", bad) {
			t.Errorf("database %q taken", bad)
		}
	}
	u := schemaOf(&userResource{})
	if !validate(t, u, "name", "a"+strings.Repeat("b", 31)) {
		t.Error("a 32-character user name refused")
	}
	for _, bad := range []string{"a" + strings.Repeat("b", 32), "A", "1a", "a-b", "default", "ezgh_admin"} {
		if validate(t, u, "name", bad) {
			t.Errorf("user %q taken", bad)
		}
	}
}
