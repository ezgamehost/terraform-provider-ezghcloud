package hostedqueues

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Broker objects (topics, vhosts, queues, exchanges, bindings, policies) share one lifecycle: a
// create, an update where the kind has one, a delete, each answered 202 and waited for. Every call
// names its own object's path; nothing lists a cluster's objects, so an object no configuration
// names is never touched (HC7).

// objectBase is what every broker object resource has.
type objectBase struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	ClusterID      types.String `tfsdk:"cluster_id"`
	Origin         types.String `tfsdk:"origin"`
}

func (b *objectBase) base() *objectBase { return b }

// brokerObject is one kind's model.
type brokerObject interface {
	base() *objectBase
	// where is the object's vhost ("" outside one) and name ("" for a binding before its create).
	where() (vhost, name string)
	setName(string)
	createBody(context.Context) (map[string]any, diag.Diagnostics)
	// updateBody is the PATCH from old, empty when nothing changed or the kind has no update.
	updateBody(ctx context.Context, old brokerObject) (map[string]any, diag.Diagnostics)
	fill(map[string]json.RawMessage)
}

// planChecker refuses, at plan, an update the API would refuse.
type planChecker interface {
	checkPlan(old brokerObject, d *diag.Diagnostics)
}

type objectKind struct {
	typeName, collection, description string
	vhosted                           bool
	nameAttr                          string // "name", or "key" for a binding
	attrs                             map[string]schema.Attribute
}

type objectResource[M any, P interface {
	*M
	brokerObject
}] struct {
	kind   objectKind
	client *client.Client
}

func (r *objectResource[M, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind.typeName
}
func (r *objectResource[M, P]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "cluster_id/name, with the vhost before the name for a RabbitMQ object."},
		"organization_id": schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "Organization managed by the configured provider."},
		"project_id":      schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project of the cluster. Changing it replaces the object."},
		"cluster_id":      schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Cluster (hqc_) it is in. Changing it replaces the object."},
		"origin":          schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "api if it was made through EZGH, broker if an app made it first (an imported object)."},
	}
	if r.kind.vhosted {
		attrs["vhost"] = schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The vhost it is in: / (or default) for the default vhost. Changing it replaces the object."}
	}
	for k, a := range r.kind.attrs {
		attrs[k] = a
	}
	resp.Schema = schema.Schema{Description: r.kind.description, Attributes: attrs}
}
func (r *objectResource[M, P]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

// vhostParam is how paths name a vhost: the default vhost / is "default".
func vhostParam(v string) string {
	if v == "/" {
		return "default"
	}
	return v
}

// path is the object's URL, or its collection's when name is "". Each segment is percent-encoded,
// so a name with / or non-ASCII letters reaches the API as one segment.
func (r *objectResource[M, P]) path(m P, withName bool) string {
	b := m.base()
	vhost, name := m.where()
	segs := []string{b.ClusterID.ValueString()}
	if r.kind.vhosted {
		segs = append(segs, "vhosts", vhostParam(vhost))
	}
	segs = append(segs, r.kind.collection)
	if withName {
		if r.kind.collection == "vhosts" {
			name = vhostParam(name)
		}
		segs = append(segs, name)
	}
	return client.ProjectPath(r.client.OrganizationID, b.ProjectID.ValueString(), "clusters", segs...)
}
func (r *objectResource[M, P]) id(m P) string {
	vhost, name := m.where()
	parts := []string{m.base().ClusterID.ValueString()}
	if r.kind.vhosted {
		parts = append(parts, vhostParam(vhost))
	}
	return strings.Join(append(parts, name), "/")
}

func (r *objectResource[M, P]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A replacement makes a new object, so the update checks don't apply. (The framework hands
	// ModifyPlan no RequiresReplace paths.)
	pv, pn := P(&plan).where()
	sv, sn := P(&state).where()
	pb, sb := P(&plan).base(), P(&state).base()
	if pv != sv || pn != sn || !pb.ProjectID.Equal(sb.ProjectID) || !pb.ClusterID.Equal(sb.ClusterID) {
		return
	}
	if c, ok := any(P(&plan)).(planChecker); ok {
		c.checkPlan(P(&state), &resp.Diagnostics)
	}
}

func (r *objectResource[M, P]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m M
	p := P(&m)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(p.base().OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body, d := p.createBody(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	b := p.base()
	b.OrganizationID, b.Origin = types.StringValue(r.client.OrganizationID), types.StringValue("api")
	// Saved only once the API took it: a refused create leaves nothing to destroy.
	saved := func(out *client.Response) bool {
		if _, name := p.where(); name == "" {
			// A binding's name is the key the API assigned: its operation target's last segment.
			var op struct{ Target struct{ ResourceName string } }
			_ = json.Unmarshal(out.Body, &op)
			rn := op.Target.ResourceName
			key, err := url.PathUnescape(rn[strings.LastIndex(rn, "/")+1:])
			if err != nil || key == "" {
				resp.Diagnostics.AddError("Binding key missing", "The API accepted the binding without naming its key.")
				return false
			}
			p.setName(key)
		}
		b.ID = types.StringValue(r.id(p))
		resp.Diagnostics.Append(setKnown(ctx, &resp.State, &m)...)
		return !resp.Diagnostics.HasError()
	}
	if err := send(ctx, r.client, "POST", r.path(p, false), body, saved); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.refresh(ctx, p, &resp.State, &resp.Diagnostics)
}

// read is the object as the API shows it; ok is false when it's gone or being deleted.
func (r *objectResource[M, P]) read(ctx context.Context, m P) (view map[string]json.RawMessage, ok bool, err error) {
	out, err := r.client.Do(ctx, service, "GET", r.path(m, true), nil)
	if err != nil {
		if missing, e := confirmedMissing(ctx, r.client, m.base().ProjectID.ValueString(), err); !missing {
			return nil, false, e
		}
		return nil, false, nil
	}
	if err = json.Unmarshal(out.Body, &view); err != nil {
		return nil, false, err
	}
	return view, string(view["pending"]) != `"delete"`, nil
}

func (r *objectResource[M, P]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m M
	p := P(&m)
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(p.base().OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	view, ok, err := r.read(ctx, p)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(p, view)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *objectResource[M, P]) fill(m P, view map[string]json.RawMessage) {
	b := m.base()
	b.ID, b.OrganizationID = types.StringValue(r.id(m)), types.StringValue(r.client.OrganizationID)
	b.Origin = rawString(view["origin"])
	m.fill(view)
}
func (r *objectResource[M, P]) refresh(ctx context.Context, m P, state *tfsdk.State, diags *diag.Diagnostics) {
	view, ok, err := r.read(ctx, m)
	if err != nil || !ok {
		// The change went through; an error here would taint the object and replace it.
		diags.AddWarning("Couldn't read the object back", "Its operation succeeded, but reading it failed. The next refresh reads it.")
		diags.Append(setKnown(ctx, state, m)...)
		return
	}
	r.fill(m, view)
	diags.Append(state.Set(ctx, m)...)
}

func (r *objectResource[M, P]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old M
	p := P(&m)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() || !validateOrg(p.base().OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body, d := p.updateBody(ctx, P(&old))
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(body) > 0 {
		if err := send(ctx, r.client, "PATCH", r.path(p, true), body, nil); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
	}
	r.refresh(ctx, p, &resp.State, &resp.Diagnostics)
}

// Delete deletes this object alone: a vhost holding queues is refused by the API, never forced.
func (r *objectResource[M, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m M
	p := P(&m)
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(p.base().OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	if err := send(ctx, r.client, "DELETE", r.path(p, true), nil, nil); err != nil {
		if missing, e := confirmedMissing(ctx, r.client, p.base().ProjectID.ValueString(), err); !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}

// ImportState takes organization_id/project_id/hqc_…/name, with the vhost before the name for a
// RabbitMQ object (default for /), each percent-encoded when it holds a /.
func (r *objectResource[M, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	keys := []string{"organization_id", "project_id", "cluster_id"}
	if r.kind.vhosted {
		keys = append(keys, "vhost")
	}
	keys = append(keys, r.kind.nameAttr)
	parts := strings.Split(req.ID, "/")
	valid := len(parts) == len(keys) && strings.HasPrefix(parts[2], "hqc_") && prefixedID.MatchString(parts[2])
	for i := range parts {
		var err error
		if parts[i], err = url.PathUnescape(parts[i]); err != nil || parts[i] == "" {
			valid = false
		}
	}
	if !valid {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected "+strings.Join(keys, "/")+", each percent-encoded when it holds a /.")
		return
	}
	if r.client != nil && parts[0] != r.client.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	id := slices.Clone(parts[2:])
	if r.kind.vhosted {
		id[1] = vhostParam(id[1])
	}
	for i, k := range keys {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.Join(id, "/"))...)
}

func rawString(raw json.RawMessage) types.String {
	var s *string
	_ = json.Unmarshal(raw, &s)
	return types.StringPointerValue(s)
}
func rawInt(raw json.RawMessage) types.Int64 {
	var n *int64
	_ = json.Unmarshal(raw, &n)
	return types.Int64PointerValue(n)
}
func rawBool(raw json.RawMessage) types.Bool {
	var b *bool
	_ = json.Unmarshal(raw, &b)
	return types.BoolPointerValue(b)
}

// jsonObject is a configured JSON object attribute, parsed; nil when unset.
func jsonObject(v types.String, name string, d *diag.Diagnostics) map[string]any {
	if !known(v) {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &out); err != nil || out == nil {
		d.AddAttributeError(path.Root(name), "Invalid JSON object", name+" must be a JSON object, as jsonencode writes one.")
	}
	return out
}

// jsonState is the API's JSON object as state keeps it: the prior text while it means the same,
// null for an empty object nothing configured.
func jsonState(prior types.String, raw json.RawMessage) types.String {
	var now, was any
	if json.Unmarshal(raw, &now) != nil || now == nil || reflect.DeepEqual(now, map[string]any{}) {
		if prior.IsNull() {
			return prior
		}
		now = map[string]any{}
	}
	if known(prior) && json.Unmarshal([]byte(prior.ValueString()), &was) == nil && reflect.DeepEqual(now, was) {
		return prior
	}
	text, _ := json.Marshal(now)
	return types.StringValue(string(text))
}
