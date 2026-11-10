package network

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A wait's limit is the timeouts block's, 20 minutes without one; a value that isn't a positive
// duration is an error, never a silent default.
func TestNATGatewayTimeouts(t *testing.T) {
	block := func(create attr.Value) types.Object {
		return types.ObjectValueMust(natTimeoutTypes, map[string]attr.Value{"create": create, "delete": types.StringNull()})
	}
	for name, c := range map[string]struct {
		timeouts types.Object
		want     time.Duration
		bad      bool
	}{
		"none":    {types.ObjectNull(natTimeoutTypes), 20 * time.Minute, false},
		"unset":   {block(types.StringNull()), 20 * time.Minute, false},
		"set":     {block(types.StringValue("45m")), 45 * time.Minute, false},
		"garbage": {block(types.StringValue("soon")), 0, true},
		"zero":    {block(types.StringValue("0s")), 0, true},
	} {
		got, diags := natGatewayModel{Timeouts: c.timeouts}.timeout(context.Background(), "create")
		if diags.HasError() != c.bad || (!c.bad && got != c.want) {
			t.Errorf("%s: %v, %v", name, got, diags)
		}
	}
}
