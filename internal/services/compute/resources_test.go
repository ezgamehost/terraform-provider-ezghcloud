package compute

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"testing"
)

func TestResourceSchemasValidate(t *testing.T) {
	for _, factory := range Resources() {
		r := factory()
		var m resource.MetadataResponse
		r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "ezghcloud"}, &m)
		var s resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &s)
		if d := s.Schema.ValidateImplementation(context.Background()); d.HasError() {
			t.Fatalf("%s: %v", m.TypeName, d)
		}
	}
	if len(Resources()) != 3 {
		t.Fatal("expected SSH key, volume, instance")
	}
}
func TestDataSourceSchemasValidate(t *testing.T) {
	for _, factory := range DataSources() {
		r := factory()
		var s datasource.SchemaResponse
		r.Schema(context.Background(), datasource.SchemaRequest{}, &s)
		if d := s.Schema.ValidateImplementation(context.Background()); d.HasError() {
			t.Fatal(d)
		}
	}
	if len(DataSources()) != 6 {
		t.Fatal("expected resource, image and type lookups")
	}
}
