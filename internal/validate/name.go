// Package validate holds plan-time checks that a name is already in the form the EZGH APIs store,
// so the provider's state never differs from what the API canonicalizes.
package validate

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var (
	hostLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	recordLabel = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

// Problem says why s is not canonical, or "". Domain names are lowercase LDH labels (punycode for
// other scripts) without a trailing dot, `*.` allowed in front when wildcard is set. A record name
// (record) is relative to a zone: @ for the apex, labels of a-z 0-9 - _, `*` allowed as the first
// label.
func Problem(s string, wildcard, record bool) string {
	if record && s == "@" {
		return ""
	}
	if s != strings.ToLower(s) {
		return "must be lowercase"
	}
	if strings.HasSuffix(s, ".") {
		return "must not end with a dot"
	}
	if wildcard || record {
		if rest, ok := strings.CutPrefix(s, "*."); ok {
			s = rest
		} else if record && s == "*" {
			return ""
		} else if strings.Contains(s, "*") {
			return "may use * only as a whole first label (*.example.com)"
		}
	}
	if s == "" || len(s) > 253 {
		return "must be 1 to 253 characters"
	}
	re := hostLabel
	if record {
		re = recordLabel
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) > 63 || !re.MatchString(l) {
			return "labels must be 1 to 63 characters of lowercase a-z, 0-9 and -" + map[bool]string{true: " or _", false: ""}[record] + " (punycode for other scripts)"
		}
	}
	return ""
}

// Name validates a string attribute (and each element of a set) with Problem.
type Name struct{ Wildcard, Record bool }

func (Name) Description(context.Context) string {
	return "must be the canonical lowercase form, without a trailing dot"
}
func (n Name) MarkdownDescription(ctx context.Context) string { return n.Description(ctx) }

func (n Name) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if p := Problem(req.ConfigValue.ValueString(), n.Wildcard, n.Record); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Name is not in canonical form", fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), p))
	}
}

func (n Name) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(interface{ ValueString() string })
		if !ok || e.IsUnknown() || e.IsNull() {
			continue
		}
		if p := Problem(s.ValueString(), n.Wildcard, n.Record); p != "" {
			resp.Diagnostics.AddAttributeError(req.Path, "Name is not in canonical form", fmt.Sprintf("%q %s.", s.ValueString(), p))
		}
	}
}
