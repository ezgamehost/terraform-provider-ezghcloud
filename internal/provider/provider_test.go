package provider

import (
	"context"
	"testing"

	framework "github.com/hashicorp/terraform-plugin-framework/provider"
)

func TestProviderSchema(t *testing.T) {
	p := New("test")()
	var resp framework.SchemaResponse
	p.Schema(context.Background(), framework.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !resp.Schema.Attributes["api_token"].IsSensitive() {
		t.Fatal("token must be sensitive")
	}
	if resp.Schema.Attributes["organization_id"] == nil {
		t.Fatal("missing organization")
	}
	var meta framework.MetadataResponse
	p.Metadata(context.Background(), framework.MetadataRequest{}, &meta)
	if meta.TypeName != "ezghcloud" || meta.Version != "test" {
		t.Fatalf("%+v", meta)
	}
}
