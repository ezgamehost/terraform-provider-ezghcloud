package waf

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// cdnService is cdn-api, which owns a distribution's waf.
const cdnService = "cdn"

type cdnDistributionWAFModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	DistributionID types.String `tfsdk:"distribution_id"`
	WafID          types.String `tfsdk:"waf_id"`
	Status         types.String `tfsdk:"status"`
}

type cdnDistributionWAFResource struct{ client *client.Client }

func (*cdnDistributionWAFResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cdn_distribution_waf"
}
func (*cdnDistributionWAFResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Attaches a WAF to a CDN distribution, where it inspects every request before the origin. A distribution has at most one WAF; destroying this detaches it.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "The distribution's ID (cdn_).", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project of the distribution and the WAF. Changing it replaces the attachment.", PlanModifiers: replace},
		"distribution_id": schema.StringAttribute{Required: true, Description: "Distribution (cdn_). Changing it replaces the attachment.", PlanModifiers: replace},
		"waf_id":          schema.StringAttribute{Required: true, Description: "WAF (waf_) in the same project. Changing it switches the distribution to the other WAF in place."},
		"status":          schema.StringAttribute{Computed: true, Description: "deploying until every location enforces the WAF's latest version, then active; failed when one couldn't. Apply doesn't wait for it."},
	}}
}
func (r *cdnDistributionWAFResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// distributionDoc is a distribution as cdn-api shows it, its ETag, and its waf.
type distributionDoc struct {
	raw  map[string]json.RawMessage
	etag string
	waf  *struct{ WafID, Status string }
}

func (r *cdnDistributionWAFResource) path(m *cdnDistributionWAFModel) string {
	return client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "distributions", m.DistributionID.ValueString())
}

func decodeDistribution(out *client.Response) (distributionDoc, error) {
	v := distributionDoc{etag: out.Header.Get("ETag")}
	err := out.Decode(&v.raw)
	if err == nil && len(v.raw["waf"]) > 0 {
		err = json.Unmarshal(v.raw["waf"], &v.waf)
	}
	return v, err
}

func (r *cdnDistributionWAFResource) read(ctx context.Context, m *cdnDistributionWAFModel) (distributionDoc, error) {
	out, err := r.client.Do(ctx, cdnService, "GET", r.path(m), nil)
	if err != nil {
		return distributionDoc{}, err
	}
	return decodeDistribution(out)
}

// spec is the distribution as a replace sends it back: every field it was read with, so the
// replace changes only waf. Rewrites go only when there are some and sign-in only when it's
// required (left out is none and off), and a bucket origin as its bucket alone.
func spec(raw map[string]json.RawMessage) (map[string]any, error) {
	var d struct {
		Name        string            `json:"name"`
		Description *string           `json:"description"`
		Tags        map[string]string `json:"tags"`
		Enabled     bool              `json:"enabled"`
		Origin      map[string]any    `json:"origin"`
		Domains     []any             `json:"domains"`
		Rewrites    []any             `json:"rewrites"`
		SSO         *struct {
			Required bool `json:"required"`
		} `json:"sso"`
	}
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	origin := map[string]any{"host": d.Origin["host"], "port": d.Origin["port"], "protocol": d.Origin["protocol"], "hostHeader": d.Origin["hostHeader"]}
	if bucket, ok := d.Origin["bucket"].(string); ok {
		origin = map[string]any{"bucket": bucket}
	}
	if d.Domains == nil {
		d.Domains = []any{}
	}
	body := map[string]any{"name": d.Name, "description": d.Description, "tags": d.Tags, "enabled": d.Enabled, "origin": origin, "domains": d.Domains}
	if len(d.Rewrites) > 0 {
		body["rewrites"] = d.Rewrites
	}
	if d.SSO != nil && d.SSO.Required {
		body["sso"] = map[string]bool{"required": true}
	}
	return body, nil
}

// set replaces the distribution as it is with its waf set (nil detaches), at the ETag read. Someone
// else's change in between (412) is read and made again, once.
func (r *cdnDistributionWAFResource) set(ctx context.Context, m *cdnDistributionWAFModel, waf *string) (distributionDoc, error) {
	for attempt := 0; ; attempt++ {
		cur, err := r.read(ctx, m)
		if err != nil {
			return cur, err
		}
		body, err := spec(cur.raw)
		if err != nil {
			return cur, err
		}
		body["waf"] = nil
		if waf != nil {
			body["waf"] = map[string]string{"wafId": *waf}
		}
		headers := http.Header{"Idempotency-Key": {idempotencyKey()}}
		if cur.etag != "" {
			headers.Set("If-Match", cur.etag)
		}
		out, err := r.client.DoWithHeaders(ctx, cdnService, "PUT", r.path(m), body, headers)
		var e *client.Error
		if errors.As(err, &e) && e.StatusCode == http.StatusPreconditionFailed && attempt == 0 {
			continue
		}
		if err != nil {
			return distributionDoc{}, err
		}
		return decodeDistribution(out)
	}
}

func (r *cdnDistributionWAFResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m cdnDistributionWAFModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.apply(ctx, &m, resp.State.Set, &resp.Diagnostics)
}
func (r *cdnDistributionWAFResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m cdnDistributionWAFModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.apply(ctx, &m, resp.State.Set, &resp.Diagnostics)
}
func (r *cdnDistributionWAFResource) apply(ctx context.Context, m *cdnDistributionWAFModel, save func(context.Context, any) diag.Diagnostics, d *diag.Diagnostics) {
	waf := m.WafID.ValueString()
	v, err := r.set(ctx, m, &waf)
	if err == nil && (v.waf == nil || v.waf.WafID != waf) {
		err = errors.New("the distribution's answer doesn't show the WAF attached")
	}
	if err != nil {
		apiError(d, err)
		return
	}
	m.ID, m.OrganizationID, m.Status = m.DistributionID, types.StringValue(r.client.OrganizationID), types.StringValue(v.waf.Status)
	d.Append(save(ctx, m)...)
}

// Read drops the attachment when the distribution is gone or has no WAF, and shows another WAF
// attached outside Terraform as a change to undo.
func (r *cdnDistributionWAFResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m cdnDistributionWAFModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := r.read(ctx, &m)
	if err != nil {
		var missing bool
		if missing, err = confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, err)
			return
		}
	}
	if err != nil || v.waf == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.ID, m.OrganizationID = m.DistributionID, types.StringValue(r.client.OrganizationID)
	m.WafID, m.Status = types.StringValue(v.waf.WafID), types.StringValue(v.waf.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete detaches the WAF (waf: null), unless the distribution is gone or has another WAF by now.
func (r *cdnDistributionWAFResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m cdnDistributionWAFModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := r.read(ctx, &m)
	if err == nil && v.waf != nil && v.waf.WafID == m.WafID.ValueString() {
		_, err = r.set(ctx, &m, nil)
	}
	if err == nil {
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *cdnDistributionWAFResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "distribution_id", "cdn_")
}
