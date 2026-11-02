package compute

import (
	"testing"
)

func TestCanonicalIdentifierUsesSlug(t *testing.T) {
	got, err := canonicalID(map[string]any{"id": "uuid-value", "slug": "ins_123456789abc"})
	if err != nil || got != "ins_123456789abc" {
		t.Fatalf("id=%q err=%v", got, err)
	}
	if _, err := canonicalID(map[string]any{"id": "uuid-value"}); err == nil {
		t.Fatal("missing prefixed identifier accepted")
	}
}

func TestSizeConversionIsExact(t *testing.T) {
	for _, s := range []string{"1", "1073741824", "9223372036854775808"} {
		if err := validateSizeBytes(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"0", "-1", "01", "1.5", "1e9", ""} {
		if err := validateSizeBytes(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestOperationTargetRetainsAssignedResource(t *testing.T) {
	id, err := operationResourceID([]byte(`{"id":"operation-id","target":{"id":"ins_123456789abc"}}`), "instance")
	if err != nil || id != "ins_123456789abc" {
		t.Fatalf("%q %v", id, err)
	}
	id, err = operationResourceID([]byte(`{"id":"operation-id","target":{"id":"uuid"},"volume":{"id":"uuid","slug":"vol_123456789abc"}}`), "volume")
	if err != nil || id != "vol_123456789abc" {
		t.Fatalf("%q %v", id, err)
	}
	if _, err := operationResourceID([]byte(`{"id":"operation-id"}`), "instance"); err == nil {
		t.Fatal("accepted operation without resource")
	}
}
