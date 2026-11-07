// Package waf implements EZGH WAF resources: WAFs, their attachment to load balancers, and the
// managed rule sets they can run. Served only with EZGHCLOUD_PREVIEW_WAF=1 until launch (Gate 3).
package waf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const service = "waf"

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &wafResource{} }, func() resource.Resource { return &loadBalancerWAFResource{} }}
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &managedRuleSetsDataSource{} }}
}

type wafModel struct {
	ID              types.String `tfsdk:"id"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	ProjectID       types.String `tfsdk:"project_id"`
	ResourceName    types.String `tfsdk:"resource_name"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Tags            types.Map    `tfsdk:"tags"`
	Status          types.String `tfsdk:"status"`
	Mode            types.String `tfsdk:"mode"`
	DefaultAction   types.String `tfsdk:"default_action"`
	ManagedRuleSets []managedSet `tfsdk:"managed_rule_sets"`
	Rules           []rule       `tfsdk:"rules"`
	Logging         *logging     `tfsdk:"logging"`
	Version         types.Int64  `tfsdk:"version"`
	Attachments     types.List   `tfsdk:"attachments"`
}
type managedSet struct {
	Name          string         `tfsdk:"name"`
	Mode          string         `tfsdk:"mode"`
	ParanoiaLevel types.Int64    `tfsdk:"paranoia_level"`
	RuleOverrides []ruleOverride `tfsdk:"rule_overrides"`
}
type ruleOverride struct {
	RuleID int64  `tfsdk:"rule_id" json:"ruleId"`
	Action string `tfsdk:"action" json:"action"`
}
type rule struct {
	Name       string       `tfsdk:"name"`
	Action     string       `tfsdk:"action"`
	Mode       types.String `tfsdk:"mode"`
	RateLimit  *rateLimit   `tfsdk:"rate_limit"`
	Conditions []condition  `tfsdk:"conditions"`
}
type rateLimit struct {
	Limit         int64 `tfsdk:"limit" json:"limit"`
	WindowSeconds int64 `tfsdk:"window_seconds" json:"windowSeconds"`
}
type condition struct {
	Type          string     `tfsdk:"type"`
	Negate        bool       `tfsdk:"negate"`
	CIDRs         []string   `tfsdk:"cidrs"`
	Numbers       []int64    `tfsdk:"numbers"`
	Codes         []string   `tfsdk:"codes"`
	Target        *string    `tfsdk:"target"`
	Value         *string    `tfsdk:"value"`
	CaseSensitive types.Bool `tfsdk:"case_sensitive"`
}
type logging struct {
	Scope       string  `tfsdk:"scope"`
	Environment *string `tfsdk:"environment"`
}

// wafView is a WAF as waf-api shows it.
type wafView struct {
	Slug, Name, ResourceName, Status, Mode, DefaultAction string
	Description                                           *string
	Tags                                                  map[string]string
	ManagedRuleSets                                       []struct {
		Name, Mode    string
		ParanoiaLevel *int64
		RuleOverrides []ruleOverride
	}
	Rules []struct {
		Name, Action string
		Mode         *string
		RateLimit    *rateLimit
		Conditions   []struct {
			Type          string
			Negate        bool
			CIDRs         []string `json:"cidrs"`
			Numbers       []int64
			Codes         []string
			Target, Value *string
			CaseSensitive *bool
		}
	}
	Logging struct {
		Scope       string
		Environment *string
	}
	Version     int64
	Attachments []string
}

// textCondition is whether a condition type matches a target's text (and so has case_sensitive).
func textCondition(t string) bool { return t == "regex" || t == "contains" || t == "equals" }

func (m *wafModel) fromAPI(ctx context.Context, v wafView, org string) {
	m.ID, m.OrganizationID, m.ResourceName = types.StringValue(v.Slug), types.StringValue(org), types.StringValue(v.ResourceName)
	m.Name, m.Description, m.Status = types.StringValue(v.Name), types.StringPointerValue(v.Description), types.StringValue(v.Status)
	m.Mode, m.DefaultAction, m.Version = types.StringValue(v.Mode), types.StringValue(v.DefaultAction), types.Int64Value(v.Version)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	if v.Attachments == nil {
		v.Attachments = []string{}
	}
	m.Attachments, _ = types.ListValueFrom(ctx, types.StringType, v.Attachments)
	m.ManagedRuleSets = []managedSet{}
	for _, s := range v.ManagedRuleSets {
		set := managedSet{Name: s.Name, Mode: s.Mode, ParanoiaLevel: types.Int64PointerValue(s.ParanoiaLevel), RuleOverrides: s.RuleOverrides}
		if set.RuleOverrides == nil {
			set.RuleOverrides = []ruleOverride{}
		}
		m.ManagedRuleSets = append(m.ManagedRuleSets, set)
	}
	m.Rules = []rule{}
	for _, r := range v.Rules {
		out := rule{Name: r.Name, Action: r.Action, Mode: types.StringPointerValue(r.Mode), RateLimit: r.RateLimit, Conditions: []condition{}}
		for _, c := range r.Conditions {
			out.Conditions = append(out.Conditions, condition{Type: c.Type, Negate: c.Negate, CIDRs: c.CIDRs, Numbers: c.Numbers, Codes: c.Codes,
				Target: c.Target, Value: c.Value, CaseSensitive: types.BoolPointerValue(c.CaseSensitive)})
		}
		m.Rules = append(m.Rules, out)
	}
	m.Logging = &logging{Scope: v.Logging.Scope, Environment: v.Logging.Environment}
}

// body is the whole WafInput: waf-api resets an omitted field to its default, even on a replace.
// A condition carries only the fields set, so one set for another type is waf-api's to refuse.
func (m *wafModel) body(ctx context.Context, d *diag.Diagnostics) map[string]any {
	tags := map[string]string{}
	d.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
	sets := []map[string]any{}
	for _, s := range m.ManagedRuleSets {
		sets = append(sets, map[string]any{"name": s.Name, "mode": s.Mode, "paranoiaLevel": s.ParanoiaLevel.ValueInt64Pointer(), "ruleOverrides": s.RuleOverrides})
	}
	rules := []map[string]any{}
	for _, r := range m.Rules {
		conditions := []map[string]any{}
		for _, c := range r.Conditions {
			out := map[string]any{"type": c.Type, "negate": c.Negate}
			if c.CIDRs != nil {
				out["cidrs"] = c.CIDRs
			}
			if c.Numbers != nil {
				out["numbers"] = c.Numbers
			}
			if c.Codes != nil {
				out["codes"] = c.Codes
			}
			if c.Target != nil {
				out["target"] = *c.Target
			}
			if c.Value != nil {
				out["value"] = *c.Value
			}
			if !c.CaseSensitive.IsNull() {
				out["caseSensitive"] = c.CaseSensitive.ValueBool()
			}
			conditions = append(conditions, out)
		}
		rules = append(rules, map[string]any{"name": r.Name, "action": r.Action, "mode": r.Mode.ValueStringPointer(), "rateLimit": r.RateLimit, "conditions": conditions})
	}
	return map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tags, "mode": m.Mode.ValueString(),
		"defaultAction": m.DefaultAction.ValueString(), "managedRuleSets": sets, "rules": rules,
		"logging": map[string]any{"scope": m.Logging.Scope, "environment": m.Logging.Environment}}
}

type wafResource struct{ client *client.Client }

func (*wafResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_waf"
}

var (
	keepString    = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	emptyList     = func(t attr.Type) defaults.List { return listdefault.StaticValue(types.ListValueMust(t, nil)) }
	loggingTypes  = map[string]attr.Type{"scope": types.StringType, "environment": types.StringType}
	overrideTypes = map[string]attr.Type{"rule_id": types.Int64Type, "action": types.StringType}
)

func (*wafResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	conditionAttrs := map[string]schema.Attribute{
		"type":           schema.StringAttribute{Required: true, Description: "ipSet, asn, country, regex, contains or equals."},
		"negate":         schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Match when the condition doesn't hold. Defaults to false."},
		"cidrs":          schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "ipSet: IPv4 and IPv6 addresses and CIDR blocks, 1 to 1,000."},
		"numbers":        schema.ListAttribute{Optional: true, ElementType: types.Int64Type, Description: "asn: autonomous system numbers the client's address is announced by, 1 to 100."},
		"codes":          schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "country: ISO 3166-1 alpha-2 codes in capitals, 1 to 250."},
		"target":         schema.StringAttribute{Optional: true, Description: "regex, contains and equals: path, query, method, body (the first 16 KB of a request body), header:<name> or cookie:<name>."},
		"value":          schema.StringAttribute{Optional: true, Description: "regex, contains and equals: what the target is compared with, at most 1,024 bytes. A regex has no backreferences or lookaround."},
		"case_sensitive": schema.BoolAttribute{Optional: true, Computed: true, Description: "regex, contains and equals: whether case matters. Defaults to false; null for other types."},
	}
	ruleAttrs := map[string]schema.Attribute{
		"name":   schema.StringAttribute{Required: true, Description: "Unique in the WAF: 1 to 64 lowercase letters, digits and dashes."},
		"action": schema.StringAttribute{Required: true, Description: "allow skips the remaining rules and the managed rule sets; block answers 403; log records the request and continues; rateLimit counts matching requests per client IP and blocks (or logs) a client over the limit."},
		"mode":   schema.StringAttribute{Optional: true, Computed: true, Description: "rateLimit rules only: block or log. Defaults to block; null for other actions."},
		"rate_limit": schema.SingleNestedAttribute{Optional: true, Description: "rateLimit rules only.", Attributes: map[string]schema.Attribute{
			"limit":          schema.Int64Attribute{Required: true, Description: "Requests a client IP may make in the window, 1 to 100,000."},
			"window_seconds": schema.Int64Attribute{Required: true, Description: "60, 300 or 600."},
		}},
		"conditions": schema.ListNestedAttribute{Required: true, Description: "1 to 5 conditions, all of which must match.", NestedObject: schema.NestedAttributeObject{Attributes: conditionAttrs}},
	}
	setAttrs := map[string]schema.Attribute{
		"name":           schema.StringAttribute{Required: true, Description: "ezgh/core (Core rule set (OWASP CRS)), ezgh/php (PHP), ezgh/node (Node.js) or ezgh/known-bad-inputs (Known bad inputs)."},
		"mode":           schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("block"), Description: "block, or log to record what the set would block without blocking it. Defaults to block."},
		"paranoia_level": schema.Int64Attribute{Optional: true, Computed: true, Description: "ezgh/core only: 1 or 2, higher catching more attacks and more false positives. Defaults to 1; null for other sets."},
		"rule_overrides": schema.ListNestedAttribute{Optional: true, Computed: true, Default: emptyList(types.ObjectType{AttrTypes: overrideTypes}),
			Description: "Rules of the set to turn off, or (except in ezgh/core) to log only, for false positives. At most 100.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"rule_id": schema.Int64Attribute{Required: true, Description: "A rule of the set."},
				"action":  schema.StringAttribute{Required: true, Description: "off or log."},
			}}},
	}
	set, r := schema.NestedAttributeObject{Attributes: setAttrs}, schema.NestedAttributeObject{Attributes: ruleAttrs}
	resp.Schema = schema.Schema{Description: "A WAF: custom rules and managed rule sets that inspect requests to the load balancers it's attached to. Attach it with ezghcloud_load_balancer_waf.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "WAF ID (waf_).", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the WAF.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"name":            schema.StringAttribute{Required: true, Description: "Name, 1 to 100 characters."},
		"description":     schema.StringAttribute{Optional: true, Description: "Description, at most 500 characters; null clears it."},
		"tags":            schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"status":          schema.StringAttribute{Computed: true, Description: "active.", PlanModifiers: keepString},
		"mode":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("block"), Description: "block, or log to record every block of the WAF without blocking. Defaults to block."},
		"default_action":  schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("allow"), Description: "What happens to a request no rule ends: allow."},
		"managed_rule_sets": schema.ListNestedAttribute{Optional: true, Computed: true, Default: emptyList(set.Type()), NestedObject: set,
			Description: "Managed rule sets, run in order after the custom rules. Each set at most once."},
		"rules": schema.ListNestedAttribute{Optional: true, Computed: true, Default: emptyList(r.Type()), NestedObject: r,
			Description: "Custom rules, top to bottom; the first that ends a request wins. At most 100."},
		"logging": schema.SingleNestedAttribute{Optional: true, Computed: true,
			Default:     objectdefault.StaticValue(types.ObjectValueMust(loggingTypes, map[string]attr.Value{"scope": types.StringValue("off"), "environment": types.StringNull()})),
			Description: "Request logs to Observability. Defaults to off.",
			Attributes: map[string]schema.Attribute{
				"scope":       schema.StringAttribute{Required: true, Description: "off, matched (the requests a rule matched) or all."},
				"environment": schema.StringAttribute{Optional: true, Description: "Observability environment in the WAF's project; required unless scope is off. Turning logging on needs obs.logs.ingest on the project."},
			}},
		"version":     schema.Int64Attribute{Computed: true, Description: "Rises on every change; updates are conditional on it."},
		"attachments": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Load balancers (lbr_) the WAF is attached to."},
	}}
}
func (r *wafResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// ModifyPlan fills the nested values waf-api derives from others, so plans stay exact: a core set's
// paranoia level, a rateLimit rule's mode, and a text condition's case sensitivity.
func (*wafResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	count := func(p path.Path) int {
		var l types.List
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, p, &l)...)
		return len(l.Elements())
	}
	for i := range count(path.Root("managed_rule_sets")) {
		p := path.Root("managed_rule_sets").AtListIndex(i)
		fillUnknown[types.Int64](ctx, req, resp, p.AtName("paranoia_level"), p.AtName("name"), func(name string) attr.Value {
			if name == "ezgh/core" {
				return types.Int64Value(1)
			}
			return types.Int64Null()
		})
	}
	for i := range count(path.Root("rules")) {
		p := path.Root("rules").AtListIndex(i)
		fillUnknown[types.String](ctx, req, resp, p.AtName("mode"), p.AtName("action"), func(action string) attr.Value {
			if action == "rateLimit" {
				return types.StringValue("block")
			}
			return types.StringNull()
		})
		for j := range count(p.AtName("conditions")) {
			c := p.AtName("conditions").AtListIndex(j)
			fillUnknown[types.Bool](ctx, req, resp, c.AtName("case_sensitive"), c.AtName("type"), func(kind string) attr.Value {
				if textCondition(kind) {
					return types.BoolValue(false)
				}
				return types.BoolNull()
			})
		}
	}
}

// fillUnknown sets the planned value at p, when unknown, to what value makes of the string at by.
func fillUnknown[T attr.Value](ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, p, by path.Path, value func(string) attr.Value) {
	var current T
	var key types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, p, &current)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, by, &key)...)
	if current.IsUnknown() && !key.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, p, value(key.ValueString()))...)
	}
}

func (r *wafResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m wafModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := m.body(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "POST", wafPath(r.client, m.ProjectID.ValueString()), body)
	r.save(ctx, &m, out, err, &resp.State, &resp.Diagnostics)
}
func (r *wafResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m wafModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	out, err := r.client.Do(ctx, service, "GET", wafPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err != nil {
		var missing bool
		if missing, err = confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); missing {
			resp.State.RemoveResource(ctx)
			return
		}
	}
	r.save(ctx, &m, out, err, &resp.State, &resp.Diagnostics)
}

// Update replaces the whole WAF, conditional on the version last read.
func (r *wafResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior wafModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := m.body(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ifMatch := http.Header{"If-Match": {strconv.Quote(strconv.FormatInt(prior.Version.ValueInt64(), 10))}}
	out, err := r.client.DoWithHeaders(ctx, service, "PUT", wafPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), body, ifMatch)
	var e *client.Error
	if errors.As(err, &e) && e.StatusCode == 412 {
		resp.Diagnostics.AddError("WAF changed since refresh", m.ID.ValueString()+" was changed outside this plan. Refresh and plan again.")
		return
	}
	r.save(ctx, &m, out, err, &resp.State, &resp.Diagnostics)
}

// Delete removes the WAF whatever its version: destroying is the intent. It fails while a load
// balancer still has it attached.
func (r *wafResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m wafModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	_, err := r.client.Do(ctx, service, "DELETE", wafPath(r.client, m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err == nil {
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *wafResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "id", "waf_")
}

// save decodes a WAF answer into state.
func (r *wafResource) save(ctx context.Context, m *wafModel, out *client.Response, err error, state *tfsdk.State, d *diag.Diagnostics) {
	var v wafView
	if err == nil {
		err = out.Decode(&v)
	}
	if err == nil && (!strings.HasPrefix(v.Slug, "waf_") || !prefixedID.MatchString(v.Slug)) {
		err = errors.New("response omitted the WAF's waf_ identity")
	}
	if err != nil {
		apiError(d, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	d.Append(state.Set(ctx, m)...)
}

func wafPath(c *client.Client, project string, id ...string) string {
	return client.ProjectPath(c.OrganizationID, project, "wafs", id...)
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

// apiError adds the API's own message and extras (field issues, the load balancers a WAF is
// attached to); an access denial keeps the provider's rule of naming only the permission.
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

// importProject takes organization_id/project_id/<prefix><12 characters> into organization_id,
// project_id, and id (and also attr, when that isn't id).
func importProject(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, c *client.Client, attr, prefix string) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[2], prefix) || !prefixedID.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/"+prefix+"<12-character ID>.")
		return
	}
	if c != nil && parts[0] != c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for k, v := range map[string]string{"organization_id": parts[0], "project_id": parts[1], "id": parts[2], attr: parts[2]} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}

// idempotencyKey is a fresh key for a write the client doesn't key itself (it keys POSTs only).
func idempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
