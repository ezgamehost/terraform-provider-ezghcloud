package certificates

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
