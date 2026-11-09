// Package cdn implements EZGH CDN resources: distributions, each serving its default hostname and
// its own domains over HTTPS in front of one origin.
package cdn

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

const service = "cdn"

// MaxDomains is cdn.domainsPerDistribution.
const MaxDomains = 10

var (
	prefixedID    = regexp.MustCompile(`^cdn_[a-z0-9]{12}$`)
	certificateID = regexp.MustCompile(`^crt_[a-z0-9]{12}$`)
)

// etagKey is where a distribution's ETag is kept in private state: its document carries no
// version, and an update is conditional on the one last read.
const etagKey = "etag"

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &distributionResource{} }}
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &distributionDataSource{} }}
}

type distributionModel struct {
	ID              types.String `tfsdk:"id"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	ProjectID       types.String `tfsdk:"project_id"`
	ResourceName    types.String `tfsdk:"resource_name"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Tags            types.Map    `tfsdk:"tags"`
	Origin          *origin      `tfsdk:"origin"`
	Domains         []domain     `tfsdk:"domain"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	DefaultHostname types.String `tfsdk:"default_hostname"`
	Status          types.String `tfsdk:"status"`
}
type origin struct {
	Host       types.String `tfsdk:"host"`
	Protocol   types.String `tfsdk:"protocol"`
	Port       types.Int64  `tfsdk:"port"`
	HostHeader types.String `tfsdk:"host_header"`
}
type domain struct {
	Hostname      types.String `tfsdk:"hostname"`
	CertificateID types.String `tfsdk:"certificate_id"`
}

// view is a distribution as cdn-api shows it.
type view struct {
	Slug, Name, ResourceName, Status, DefaultHostname string
	Description                                       *string
	Tags                                              map[string]string
	Origin                                            struct {
		Host, Protocol, HostHeader string
		Port                       int64
	}
	Domains []struct{ Hostname, CertificateID string }
	Enabled bool
}

func (m *distributionModel) fromAPI(ctx context.Context, v view, org string) {
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.Name, m.Description, m.Status = types.StringValue(v.Name), types.StringPointerValue(v.Description), types.StringValue(v.Status)
	m.DefaultHostname, m.Enabled = types.StringValue(v.DefaultHostname), types.BoolValue(v.Enabled)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.Origin = &origin{Host: types.StringValue(v.Origin.Host), Protocol: types.StringValue(v.Origin.Protocol), Port: types.Int64Value(v.Origin.Port),
		HostHeader: types.StringValue(v.Origin.HostHeader)}
	// No domain is null, as no block is in config.
	m.Domains = nil
	for _, d := range v.Domains {
		m.Domains = append(m.Domains, domain{Hostname: types.StringValue(d.Hostname), CertificateID: types.StringValue(d.CertificateID)})
	}
}

// body is the whole DistributionSpec: cdn-api resets an omitted field to its default, even on a
// replace.
func (m *distributionModel) body(ctx context.Context, d *diag.Diagnostics) map[string]any {
	tags := map[string]string{}
	d.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	domains := []map[string]string{}
	for _, x := range m.Domains {
		domains = append(domains, map[string]string{"hostname": x.Hostname.ValueString(), "certificateId": x.CertificateID.ValueString()})
	}
	o := m.Origin
	return map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags, "enabled": m.Enabled.ValueBool(),
		"origin":  map[string]any{"host": o.Host.ValueString(), "protocol": o.Protocol.ValueString(), "port": o.Port.ValueInt64(), "hostHeader": o.HostHeader.ValueString()},
		"domains": domains}
}

type distributionResource struct{ client *client.Client }

func (*distributionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cdn_distribution"
}

var keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

func (*distributionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A CDN distribution: serves its default hostname and your own domains over HTTPS from locations near your visitors, and forwards each request to one origin. CNAME each domain to default_hostname.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, Description: "Distribution ID (cdn_).", PlanModifiers: keepString},
			"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
			"project_id":      schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the distribution.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
			"name":            schema.StringAttribute{Required: true, Description: "Name, 1 to 100 characters."},
			"description":     schema.StringAttribute{Optional: true, Description: "Description, at most 1,000 characters; null clears it."},
			"tags": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
			"origin": schema.SingleNestedAttribute{Required: true, Description: "Where requests go.", Attributes: map[string]schema.Attribute{
				"host":        schema.StringAttribute{Required: true, Description: "A hostname or IPv4 address on the internet, in lowercase. Private, loopback and EZGH platform addresses are refused."},
				"protocol":    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("https"), Description: "https or http. With https the origin's certificate must be valid for host. Defaults to https."},
				"port":        schema.Int64Attribute{Optional: true, Computed: true, Description: "1 to 65535. Defaults to 443 for https and 80 for http."},
				"host_header": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("viewer"), Description: "The Host header the origin gets: viewer (the visitor's) or origin (host). Defaults to viewer."},
			}},
			"enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "false keeps the distribution and serves nothing for it. Defaults to true."},
			"default_hostname": schema.StringAttribute{Computed: true, Description: "d<12>.ezghcdn.com: the hostname it serves at once, and the CNAME target of its domains. Never changes.", PlanModifiers: keepString},
			"status":           schema.StringAttribute{Computed: true, Description: "deploying after every change until every location has it, then deployed."},
		},
		Blocks: map[string]schema.Block{
			"domain": schema.ListNestedBlock{Description: "One of your own domains, at most 10. Attaching its certificate needs certificates.certificates.use on it.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"hostname":       schema.StringAttribute{Required: true, Description: "An exact hostname (www.example.com) or a one-label wildcard (*.example.com), in lowercase. One distribution serves a hostname at a time."},
					"certificate_id": schema.StringAttribute{Required: true, Description: "A Web Certificates certificate (crt_) in the same project that covers hostname."},
				}}},
		},
	}
}
func (r *distributionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ValidateConfig refuses at plan what cdn-api would refuse at apply and can be known from config.
// The origin object or the domain list as a whole may be unknown (built from other resources):
// what's unknown waits for apply.
func (*distributionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m distributionModel
	var o types.Object
	var domains types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &m.Name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("origin"), &o)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("domain"), &domains)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(o) {
		m.Origin = &origin{}
		resp.Diagnostics.Append(o.As(ctx, m.Origin, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	}
	if known(domains) {
		resp.Diagnostics.Append(domains.ElementsAs(ctx, &m.Domains, false)...)
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(validate(m)...)
	}
}

func validate(m distributionModel) diag.Diagnostics {
	var d diag.Diagnostics
	// cdn-api trims a name, which would read back different from config.
	if known(m.Name) && (strings.TrimSpace(m.Name.ValueString()) != m.Name.ValueString() || m.Name.ValueString() == "") {
		d.AddAttributeError(path.Root("name"), "Invalid name", "Give 1 to 100 characters, without spaces at either end.")
	}
	one := func(v types.String, p path.Path, allowed ...string) {
		if known(v) && !slices.Contains(allowed, v.ValueString()) {
			d.AddAttributeError(p, "Invalid value", fmt.Sprintf("Must be %s.", strings.Join(allowed, " or ")))
		}
	}
	if o := m.Origin; o != nil {
		one(o.Protocol, path.Root("origin").AtName("protocol"), "https", "http")
		one(o.HostHeader, path.Root("origin").AtName("host_header"), "viewer", "origin")
		if known(o.Port) && (o.Port.ValueInt64() < 1 || o.Port.ValueInt64() > 65535) {
			d.AddAttributeError(path.Root("origin").AtName("port"), "Invalid port", "Must be from 1 to 65535.")
		}
	}
	if len(m.Domains) > MaxDomains {
		d.AddAttributeError(path.Root("domain"), "Too many domains", fmt.Sprintf("A distribution has at most %d domains; this has %d.", MaxDomains, len(m.Domains)))
	}
	seen := map[string]bool{}
	for i, x := range m.Domains {
		p := path.Root("domain").AtListIndex(i)
		if known(x.Hostname) {
			h := x.Hostname.ValueString()
			if h != strings.ToLower(h) || strings.HasSuffix(h, ".") {
				d.AddAttributeError(p.AtName("hostname"), "Invalid hostname", "Give it in lowercase, without a trailing dot.")
			}
			if seen[h] {
				d.AddAttributeError(p.AtName("hostname"), "Duplicate domain", h+" is given twice.")
			}
			seen[h] = true
		}
		if known(x.CertificateID) && !certificateID.MatchString(x.CertificateID.ValueString()) {
			d.AddAttributeError(p.AtName("certificate_id"), "Invalid certificate", "Must be a crt_ ID.")
		}
	}
	return d
}

// ModifyPlan fills an origin's port from its protocol when config leaves it out, so plans stay
// exact.
func (*distributionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var port types.Int64
	var protocol types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("origin").AtName("port"), &port)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("origin").AtName("protocol"), &protocol)...)
	if resp.Diagnostics.HasError() || !port.IsUnknown() || protocol.IsUnknown() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("origin").AtName("port"), defaultPort(protocol.ValueString()))...)
}

func defaultPort(protocol string) int64 {
	if protocol == "http" {
		return 80
	}
	return 443
}

func (r *distributionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m distributionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := m.body(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", distributionPath(r.client, m.ProjectID.ValueString()), body)
	r.save(ctx, &m, out, err, &resp.State, resp.Private, &resp.Diagnostics)
}
func (r *distributionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m distributionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	out, err := r.client.Do(ctx, service, "GET", distributionPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err != nil {
		var missing bool
		if missing, err = confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); missing {
			resp.State.RemoveResource(ctx)
			return
		}
	}
	r.save(ctx, &m, out, err, &resp.State, resp.Private, &resp.Diagnostics)
}

// Update replaces the whole distribution, conditional on the ETag last read.
func (r *distributionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m distributionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := m.body(ctx, &resp.Diagnostics)
	etag, diags := req.Private.GetKey(ctx, etagKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var headers http.Header
	if len(etag) > 0 {
		headers = http.Header{"If-Match": {string(etag)}}
	}
	out, err := r.client.DoWithHeaders(ctx, service, "PUT", distributionPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), body, headers)
	var e *client.Error
	if errors.As(err, &e) && e.StatusCode == 412 {
		resp.Diagnostics.AddError("Distribution changed since refresh", m.ID.ValueString()+" was changed outside this plan. Refresh and plan again.")
		return
	}
	r.save(ctx, &m, out, err, &resp.State, resp.Private, &resp.Diagnostics)
}

// Delete removes the distribution whatever its version: destroying is the intent.
func (r *distributionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m distributionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	_, err := r.client.Do(ctx, service, "DELETE", distributionPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err == nil {
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *distributionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !prefixedID.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/cdn_<12-character ID>.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for k, v := range map[string]string{"organization_id": parts[0], "project_id": parts[1], "id": parts[2]} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}

// privateState is where save keeps the ETag (resource.*Response.Private).
type privateState interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

// state is where save puts the distribution.
type state interface {
	Set(context.Context, any) diag.Diagnostics
}

// save decodes a distribution answer into state, and its ETag into private state.
func (r *distributionResource) save(ctx context.Context, m *distributionModel, out *client.Response, err error, s state, private privateState, d *diag.Diagnostics) {
	var v view
	if err == nil {
		err = out.Decode(&v)
	}
	if err == nil && !prefixedID.MatchString(v.Slug) {
		err = errors.New("response omitted the distribution's cdn_ identity")
	}
	if err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	d.Append(s.Set(ctx, m)...)
	if etag := out.Header.Get("ETag"); etag != "" && private != nil {
		d.Append(private.SetKey(ctx, etagKey, []byte(etag))...)
	}
}

func distributionPath(c *client.Client, project string, id ...string) string {
	return client.ProjectPath(c.OrganizationID, project, "distributions", id...)
}

// distributionDataSource reads one distribution.
type distributionDataSource struct{ client *client.Client }

func (*distributionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cdn_distribution"
}
func (*distributionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = dschema.Schema{Description: "A CDN distribution, by ID.", Attributes: map[string]dschema.Attribute{
		"id":               dschema.StringAttribute{Required: true, Description: "Distribution ID (cdn_)."},
		"project_id":       dschema.StringAttribute{Required: true, Description: "Project identifier."},
		"organization_id":  computed("Organization managed by the configured provider."),
		"resource_name":    computed("IAM resource name."),
		"name":             computed("Name."),
		"description":      computed("Description."),
		"tags":             dschema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Resource tags."},
		"default_hostname": computed("d<12>.ezghcdn.com: the CNAME target of its domains."),
		"status":           computed("deploying or deployed."),
		"enabled":          dschema.BoolAttribute{Computed: true, Description: "Whether it serves its hostnames."},
		"origin": dschema.SingleNestedAttribute{Computed: true, Description: "Where requests go.", Attributes: map[string]dschema.Attribute{
			"host": computed("Hostname or IPv4 address."), "protocol": computed("https or http."), "host_header": computed("viewer or origin."),
			"port": dschema.Int64Attribute{Computed: true, Description: "Port."},
		}},
		"domain": dschema.ListNestedAttribute{Computed: true, Description: "Its own domains.", NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
			"hostname": computed("Hostname."), "certificate_id": computed("Web Certificates certificate (crt_)."),
		}}},
	}}
}
func (d *distributionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &d.client)
}
func (d *distributionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m distributionModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &m.ID)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &m.ProjectID)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, d.client, &resp.Diagnostics) {
		return
	}
	out, err := d.client.Do(ctx, service, "GET", distributionPath(d.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	r := distributionResource{client: d.client}
	r.save(ctx, &m, out, err, &resp.State, nil, &resp.Diagnostics)
	if m.Domains == nil && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("domain"), []domain{})...)
	}
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

// apiError adds the API's own message and extras (field issues); an access denial keeps the
// provider's rule of naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	detail := err.Error()
	var e *client.Error
	if errors.As(err, &e) && e.Code != "access_denied" {
		detail += "\n" + e.Message
		for _, k := range slices.Sorted(maps.Keys(e.Details)) {
			detail += "\n" + k + ": " + string(e.Details[k])
		}
	}
	d.AddError("EZGH Cloud API request failed", detail)
}

// confirmedMissing tells a missing resource (404 in a readable project) from a hidden project.
func confirmedMissing(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	var e *client.Error
	if !errors.As(err, &e) || e.StatusCode != 404 {
		return false, err
	}
	if _, parentErr := c.Do(ctx, "orgs", "GET", client.OrgPath(c.OrganizationID, "projects", project), nil); parentErr != nil {
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
