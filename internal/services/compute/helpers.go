// Package compute implements Compute and Block Storage resources through public APIs.
package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)
var byteCount = regexp.MustCompile(`^[1-9][0-9]*$`)

func canonicalID(v map[string]any) (string, error) {
	for _, k := range []string{"slug", "id"} {
		if s, ok := v[k].(string); ok && prefixedID.MatchString(s) {
			return s, nil
		}
	}
	return "", fmt.Errorf("API response omitted its canonical prefixed resource identifier")
}
func validateSizeBytes(s string) error {
	if !byteCount.MatchString(s) {
		return fmt.Errorf("size_bytes must be an exact positive decimal byte count")
	}
	return nil
}
func operationResourceID(body []byte, kind string) (string, error) {
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("invalid operation response")
	}
	if obj, ok := v[kind].(map[string]any); ok {
		if id, err := canonicalID(obj); err == nil {
			return id, nil
		}
	}
	if target, ok := v["target"].(map[string]any); ok {
		if id, err := canonicalID(target); err == nil {
			return id, nil
		}
		if rn, ok := target["resourceName"].(string); ok {
			id := rn[strings.LastIndex(rn, ":")+1:]
			if prefixedID.MatchString(id) {
				return id, nil
			}
		}
		// Older public APIs expose only the target UUID. Preserve it immediately;
		// the first resource read replaces it with the canonical prefixed ID.
		if id, ok := target["id"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("operation response omitted assigned %s identity", kind)
}
func stringMap(ctx context.Context, v types.Map) (map[string]string, diag.Diagnostics) {
	out := map[string]string{}
	d := v.ElementsAs(ctx, &out, false)
	return out, d
}
func mapValue(ctx context.Context, v map[string]string) types.Map {
	if v == nil {
		v = map[string]string{}
	}
	out, _ := types.MapValueFrom(ctx, types.StringType, v)
	return out
}
func stringsValue(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	out, _ := types.ListValueFrom(ctx, types.StringType, v)
	return out
}
func nullableString(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}
func configure(req resource.ConfigureRequest, d *diag.Diagnostics, c **client.Client) {
	if req.ProviderData == nil {
		return
	}
	v, ok := req.ProviderData.(*client.Client)
	if !ok {
		d.AddError("Invalid provider configuration", "Expected an EZGH Cloud API client.")
		return
	}
	*c = v
}
func apiError(d *diag.Diagnostics, err error) {
	d.AddError("EZGH Cloud API request failed", err.Error())
}
func isNotFound(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.StatusCode == 404
}

// A parent read distinguishes a missing resource from a hidden/inaccessible project.
func confirmedMissing(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	if !isNotFound(err) {
		return false, err
	}
	_, parentErr := c.Do(ctx, "orgs", "GET", client.OrgPath(c.OrganizationID, "projects", project), nil)
	if parentErr != nil {
		return false, fmt.Errorf("cannot confirm resource absence because its project is inaccessible: %w", parentErr)
	}
	return true, nil
}
func validateOrg(org types.String, c *client.Client, d *diag.Diagnostics) bool {
	if c == nil {
		d.AddError("Provider not configured", "Configure the EZGH Cloud provider before using resources.")
		return false
	}
	if !org.IsNull() && !org.IsUnknown() && org.ValueString() != c.OrganizationID {
		d.AddError("Organization mismatch", "The resource organization_id must match the configured provider organization.")
		return false
	}
	return true
}
func timeoutContext(ctx context.Context, v types.String, defaultDuration string, d *diag.Diagnostics) (context.Context, context.CancelFunc) {
	s := v.ValueString()
	if s == "" {
		s = defaultDuration
	}
	duration, err := parseTimeout(s)
	if err != nil {
		d.AddError("Invalid operation timeout", err.Error())
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, duration)
}
func emptyList(t attr.Type) types.List { return types.ListNull(t) }
