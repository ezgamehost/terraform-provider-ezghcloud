// Package databases implements EZGH Databases resources: databases and their manual backups.
package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const service = "databases"

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &databaseResource{} }, func() resource.Resource { return &backupResource{} }}
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &lookup{kind: "database"} }, func() datasource.DataSource { return &lookup{kind: "database_backup"} }}
}

// operationResourceID is the slug that ends the operation target's resource name.
func operationResourceID(body []byte) (string, error) {
	var v struct{ Target struct{ ResourceName string } }
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("invalid operation response")
	}
	rn := v.Target.ResourceName
	id := rn[strings.LastIndexAny(rn, ":/")+1:]
	if !prefixedID.MatchString(id) {
		return "", fmt.Errorf("operation response omitted the assigned resource identity")
	}
	return id, nil
}
func operationID(out *client.Response) types.String {
	var v struct{ ID string }
	if out.Decode(&v) != nil {
		return types.StringValue("")
	}
	return types.StringValue(v.ID)
}
func waitResponse(ctx context.Context, c *client.Client, out *client.Response) error {
	if out.StatusCode != 202 {
		return nil
	}
	location := out.Header.Get("Location")
	if location == "" {
		return fmt.Errorf("accepted operation omitted Location; resource identity retained")
	}
	return c.Wait(ctx, service, location)
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
func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

// setKnown saves m with every value still unknown set to null, so identity survives a failed apply.
func setKnown(ctx context.Context, s *tfsdk.State, m any) diag.Diagnostics {
	d := s.Set(ctx, m)
	if d.HasError() {
		return d
	}
	raw, err := tftypes.Transform(s.Raw, func(_ *tftypes.AttributePath, v tftypes.Value) (tftypes.Value, error) {
		if !v.IsKnown() {
			return tftypes.NewValue(v.Type(), nil), nil
		}
		return v, nil
	})
	if err != nil {
		d.AddError("Cannot save resource state", err.Error())
		return d
	}
	s.Raw = raw
	return d
}
func configure(data any, d *diag.Diagnostics, c **client.Client) {
	if data == nil {
		return
	}
	v, ok := data.(*client.Client)
	if !ok {
		d.AddError("Invalid provider configuration", "Expected an EZGH Cloud API client.")
		return
	}
	*c = v
}

// apiError adds databases-api's own message and extras (restorable ranges, precheck reasons, field
// issues); an access denial keeps the provider's rule of naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	detail := err.Error()
	var e *client.Error
	if errors.As(err, &e) && e.Code != "access_denied" && e.Message != "" {
		detail += "\n" + e.Message
		for _, k := range slices.Sorted(maps.Keys(e.Details)) {
			detail += "\n" + k + ": " + string(e.Details[k])
		}
	}
	d.AddError("EZGH Cloud API request failed", detail)
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
func timeoutContext(ctx context.Context, v types.String, d *diag.Diagnostics) (context.Context, context.CancelFunc) {
	s := v.ValueString()
	if s == "" {
		s = defaultTimeout
	}
	duration, err := time.ParseDuration(s)
	if err != nil || duration <= 0 {
		d.AddError("Invalid operation timeout", "operation_timeout must be a positive duration, for example 60m")
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, duration)
}

const defaultTimeout = "60m"

func importProject(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, c *client.Client, prefix string) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], prefix) || !prefixedID.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/"+prefix+"<12-character ID>.")
		return
	}
	if c != nil && parts[0] != c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range []string{"organization_id", "project_id", "id"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
}
