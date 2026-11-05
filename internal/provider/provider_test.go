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

// Files isn't launched: it's served only on the EZGHCLOUD_PREVIEW_FILES=1 opt-in.
func TestFilesOnlyOnPreviewOptIn(t *testing.T) {
	count := func() int { return len(New("test")().Resources(context.Background())) }
	t.Setenv("EZGHCLOUD_PREVIEW_FILES", "")
	without := count()
	t.Setenv("EZGHCLOUD_PREVIEW_FILES", "1")
	if with := count(); with != without+3 {
		t.Fatalf("%d resources with the opt-in, %d without", with, without)
	}
}
