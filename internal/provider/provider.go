// Package provider wires EZGH Cloud services into the Terraform plugin protocol.
package provider

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/caches"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/cdn"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/certificates"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/compute"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/databases"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/dns"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/files"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/iam"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/kubernetes"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/network"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/services/waf"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	framework "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type cloudProvider struct{ version string }
type configuration struct {
	Token        types.String `tfsdk:"api_token"`
	Organization types.String `tfsdk:"organization_id"`
	Endpoints    types.Map    `tfsdk:"endpoints"`
}

func New(version string) func() framework.Provider {
	return func() framework.Provider { return &cloudProvider{version: version} }
}
func (p *cloudProvider) Metadata(_ context.Context, _ framework.MetadataRequest, resp *framework.MetadataResponse) {
	resp.TypeName = "ezghcloud"
	resp.Version = p.version
}

// cdnPreview is whether EZGH CDN, not launched yet, is served: only with EZGHCLOUD_PREVIEW_CDN=1
// (its tests set it), so releases and their Registry docs leave it out. At launch, drop it and run
// make docs.
func cdnPreview() bool { return os.Getenv("EZGHCLOUD_PREVIEW_CDN") == "1" }

func (p *cloudProvider) Schema(_ context.Context, _ framework.SchemaRequest, resp *framework.SchemaResponse) {
	services := "orgs, compute, network, firewall, blockstore, images, databases, caches, files, dns, certificates, kubernetes, lb, waf"
	if cdnPreview() {
		services += ", cdn"
	}
	resp.Schema = schema.Schema{Description: "Manage EZGH Cloud infrastructure and IAM through its public APIs.", Attributes: map[string]schema.Attribute{
		"api_token":       schema.StringAttribute{Optional: true, Sensitive: true, Description: "Customer bearer credential. Defaults to EZGHCLOUD_API_TOKEN. The credential is never stored as a resource attribute."},
		"organization_id": schema.StringAttribute{Optional: true, Description: "Organization identifier. Defaults to EZGHCLOUD_ORGANIZATION_ID. Use provider aliases for multiple organizations."},
		"endpoints":       schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Optional API base URL overrides by service: " + services + ". Credentials are sent to these URLs; use trusted endpoints only. Defaults to public HTTPS APIs."},
	}}
}
func (p *cloudProvider) Configure(ctx context.Context, req framework.ConfigureRequest, resp *framework.ConfigureResponse) {
	var cfg configuration
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Token.IsUnknown() || cfg.Organization.IsUnknown() || cfg.Endpoints.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "API token, organization and endpoints must be known before managing resources.")
		return
	}
	token := cfg.Token.ValueString()
	if cfg.Token.IsNull() {
		token = os.Getenv("EZGHCLOUD_API_TOKEN")
	}
	org := cfg.Organization.ValueString()
	if cfg.Organization.IsNull() {
		org = os.Getenv("EZGHCLOUD_ORGANIZATION_ID")
	}
	if strings.TrimSpace(token) == "" {
		resp.Diagnostics.AddError("Missing API token", "Set api_token or EZGHCLOUD_API_TOKEN.")
	}
	if strings.TrimSpace(org) == "" {
		resp.Diagnostics.AddError("Missing organization", "Set organization_id or EZGHCLOUD_ORGANIZATION_ID.")
	}
	endpoints := map[string]string{}
	if !cfg.Endpoints.IsNull() {
		resp.Diagnostics.Append(cfg.Endpoints.ElementsAs(ctx, &endpoints, false)...)
	}
	for service, endpoint := range endpoints {
		if _, ok := client.DefaultEndpoints[service]; !ok || (service == "cdn" && !cdnPreview()) {
			resp.Diagnostics.AddError("Unknown API service", fmt.Sprintf("Unsupported endpoint key %q.", service))
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.Trim(u.Path, "/") != "" {
			resp.Diagnostics.AddError("Invalid API endpoint", fmt.Sprintf("Endpoint for %s must be an HTTP or HTTPS origin without credentials, path, query or fragment.", service))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c := client.New(client.Config{OrganizationID: org, Token: token, Version: p.version, Endpoints: endpoints})
	resp.ResourceData = c
	resp.DataSourceData = c
}
func (p *cloudProvider) Resources(context.Context) []func() resource.Resource {
	out := []func() resource.Resource{func() resource.Resource { return &projectResource{} }}
	out = append(out, iam.Resources()...)
	out = append(out, network.Resources()...)
	out = append(out, databases.Resources()...)
	out = append(out, caches.Resources()...)
	out = append(out, files.Resources()...)
	out = append(out, dns.Resources()...)
	out = append(out, certificates.Resources()...)
	out = append(out, waf.Resources()...)
	if cdnPreview() {
		out = append(out, cdn.Resources()...)
	}
	out = append(out, kubernetes.Resources()...)
	return append(out, compute.Resources()...)
}
func (p *cloudProvider) DataSources(context.Context) []func() datasource.DataSource {
	out := []func() datasource.DataSource{func() datasource.DataSource { return &projectDataSource{} }}
	out = append(out, iam.DataSources()...)
	out = append(out, network.DataSources()...)
	out = append(out, databases.DataSources()...)
	out = append(out, caches.DataSources()...)
	out = append(out, files.DataSources()...)
	out = append(out, dns.DataSources()...)
	out = append(out, waf.DataSources()...)
	if cdnPreview() {
		out = append(out, cdn.DataSources()...)
	}
	out = append(out, kubernetes.DataSources()...)
	return append(out, compute.DataSources()...)
}
