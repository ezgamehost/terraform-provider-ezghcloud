package certificates

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func TestWarnManagedDNS(t *testing.T) {
	detail := "Link the zone again"
	var failed authorizationView
	failed.ManagedDNS = &struct {
		ZoneID, Status string
		Detail         *string
	}{"zon_000000000001", "failed", &detail}
	var d diag.Diagnostics
	warnManagedDNS(&d, failed)
	if d.WarningsCount() != 1 || !strings.Contains(d[0].Detail(), detail) || d.HasError() {
		t.Fatalf("failed status: %v", d)
	}
	failed.ManagedDNS.Status = "synced"
	d = nil
	warnManagedDNS(&d, failed)
	warnManagedDNS(&d, authorizationView{})
	if len(d) != 0 {
		t.Fatalf("synced or unlinked warned: %v", d)
	}
}

// Unlinking releases the records: they stay in the zone, the customer's own.
func TestDNSZoneIDSaysRecordsStay(t *testing.T) {
	var resp resource.SchemaResponse
	(&authorizationResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if d := resp.Schema.Attributes["dns_zone_id"].GetDescription(); !strings.Contains(d, "stay in it as your own") || strings.Contains(d, "removes") {
		t.Fatalf("dns_zone_id description: %s", d)
	}
}
