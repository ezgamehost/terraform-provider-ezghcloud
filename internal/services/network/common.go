// Package network implements VPC, subnet and authoritative firewall resources.
package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func Resources() []func() resource.Resource {
	out := []func() resource.Resource{func() resource.Resource { return &vpcResource{} }, func() resource.Resource { return &subnetResource{} }, func() resource.Resource { return &firewallResource{} }}
	if natPreview() {
		out = append(out, func() resource.Resource { return &natGatewayResource{} })
	}
	return out
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &lookup{kind: "vpc"} }, func() datasource.DataSource { return &lookup{kind: "subnet"} }, func() datasource.DataSource { return &lookup{kind: "firewall"} }}
}

func parseImport(raw, prefix string) (string, string, string, error) {
	p := strings.Split(raw, "/")
	if len(p) != 3 || p[0] == "" || p[1] == "" || !strings.HasPrefix(p[2], prefix) || len(p[2]) != len(prefix)+12 {
		return "", "", "", fmt.Errorf("expected organization_id/project_id/%s<12-character ID>", prefix)
	}
	for _, c := range p[2][len(prefix):] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return "", "", "", fmt.Errorf("resource ID must be canonical")
		}
	}
	return p[0], p[1], p[2], nil
}

func baseAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "Canonical prefixed resource ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization owning this resource.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project owning this resource.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":            schema.StringAttribute{Required: true, Description: "Resource name."},
		"description":     schema.StringAttribute{Optional: true, Description: "Optional resource description; null clears it."},
		"tags":            schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Customer resource tags."},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name."},
		"status":          schema.StringAttribute{Computed: true, Description: "Current service status."},
	}
}

func readGone(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	var e *client.Error
	if !errors.As(err, &e) || e.StatusCode != http.StatusNotFound {
		return false, err
	}
	// Service handlers authorize missing resource reads before returning 404. Verify
	// the project too so organization-hiding 404s never discard valid state.
	if _, parentErr := c.Do(ctx, "orgs", "GET", client.OrgPath(c.OrganizationID, "projects", project), nil); parentErr != nil {
		return false, fmt.Errorf("resource returned 404 but parent access could not be verified: %w", parentErr)
	}
	return true, nil
}

func checkOrg(c *client.Client, org types.String) error {
	if !org.IsNull() && !org.IsUnknown() && org.ValueString() != c.OrganizationID {
		return fmt.Errorf("resource organization %q differs from configured organization %q", org.ValueString(), c.OrganizationID)
	}
	return nil
}

func deleteResource(ctx context.Context, c *client.Client, service, project, collection, id string) error {
	_, err := c.Do(ctx, service, "DELETE", client.ProjectPath(c.OrganizationID, project, collection, id), nil)
	if err != nil {
		gone, e := readGone(ctx, c, project, err)
		if gone {
			return nil
		}
		return e
	}
	if collection != "vpcs" {
		return nil
	}
	// A successful VPC delete can leave withdrawal work outstanding. Wait for its
	// explicit absence; do not issue another delete against a transitional resource.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		response, readErr := c.Do(ctx, service, "GET", client.ProjectPath(c.OrganizationID, project, collection, id), nil)
		err = readErr
		if err != nil {
			gone, e := readGone(ctx, c, project, err)
			if gone {
				return nil
			}
			return e
		}
		var current vpcWire
		if err = response.Decode(&current); err != nil {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("VPC %s deletion is still pending: %w", id, ctx.Err())
		case <-timer.C:
		}
	}
}
