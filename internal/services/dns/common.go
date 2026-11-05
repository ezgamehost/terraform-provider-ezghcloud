// Package dns implements EZGH DNS resources: zones and record sets.
package dns

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const service = "dns"

var zoneID = regexp.MustCompile(`^zon_[a-z0-9]{12}$`)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &zoneResource{} }, func() resource.Resource { return &recordSetResource{} }}
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &zoneDataSource{} }}
}

func zonesPath(c *client.Client, project string, rest ...string) string {
	return client.ProjectPath(c.OrganizationID, project, "zones", rest...)
}

func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

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

// summaries name the refusals DNS answers for managed record sets, so the diagnostic says what
// happened instead of a generic failure.
var summaries = map[string]string{
	"managed_record_set":       "Record set is managed by another EZGH service",
	"zone_has_managed_records": "Zone has managed record sets",
	"record_set_exists":        "Record set already exists",
}

// apiError adds dns-api's own message and extras; an access denial keeps the provider's rule of
// naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	summary, detail := "EZGH Cloud API request failed", err.Error()
	var e *client.Error
	if errors.As(err, &e) && e.Code != "access_denied" {
		if s, ok := summaries[e.Code]; ok {
			summary = s
		}
		detail += "\n" + e.Message
		for _, k := range slices.Sorted(maps.Keys(e.Details)) {
			detail += "\n" + k + ": " + string(e.Details[k])
		}
	}
	d.AddError(summary, detail)
}

// confirmedMissing tells a missing resource from a hidden or inaccessible project.
func confirmedMissing(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	var e *client.Error
	if !errors.As(err, &e) || e.StatusCode != 404 {
		return false, err
	}
	if _, perr := c.Do(ctx, "orgs", "GET", client.OrgPath(c.OrganizationID, "projects", project), nil); perr != nil {
		return false, fmt.Errorf("cannot confirm resource absence because its project is inaccessible: %w", perr)
	}
	return true, nil
}

func validateZoneID(id string) error {
	if !zoneID.MatchString(id) {
		return fmt.Errorf("%q is not a zone ID (zon_ followed by 12 characters)", url.PathEscape(id))
	}
	return nil
}

func missingProvider(d *diag.Diagnostics, c *client.Client) bool {
	if c == nil {
		d.AddError("Provider not configured", "Configure the EZGH Cloud provider before using resources.")
		return true
	}
	return false
}

func tagsOf(ctx context.Context, v types.Map, d *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	d.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}
