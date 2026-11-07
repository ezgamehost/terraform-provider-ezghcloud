// Package kubernetes implements EZGH Kubernetes resources: clusters, node groups, add-ons, access
// entries and pod identity associations.
package kubernetes

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const service = "kubernetes"

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return &clusterResource{} }, func() resource.Resource { return &nodeGroupResource{} },
		func() resource.Resource { return &addonResource{} }, func() resource.Resource { return &accessEntryResource{} },
		func() resource.Resource { return &associationResource{} },
	}
}
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &clusterDataSource{} }}
}

var (
	keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace    = []planmodifier.String{stringplanmodifier.RequiresReplace()}
)

// clusterPath is a cluster's path, or a collection's under it: clusterPath(c, prj, kcl, "node-groups", kng).
func clusterPath(c *client.Client, project, cluster string, rest ...string) string {
	return client.ProjectPath(c.OrganizationID, project, "clusters", append([]string{cluster}, rest...)...)
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

// get reads one object into v and checks its slug has prefix.
func get(ctx context.Context, c *client.Client, p, prefix string, v any) error {
	out, err := c.Do(ctx, service, "GET", p, nil)
	if err != nil {
		return err
	}
	if err = out.Decode(v); err != nil {
		return err
	}
	var s struct{ Slug string }
	_ = out.Decode(&s)
	if !strings.HasPrefix(s.Slug, prefix) || !prefixedID.MatchString(s.Slug) {
		return fmt.Errorf("response omitted its %s identity", prefix)
	}
	return nil
}

// remove DELETEs p and waits until it's gone. A delete already in progress (an earlier apply's that
// was cut short) is waited out; a 404 is gone.
func remove(ctx context.Context, c *client.Client, p string) error {
	out, err := c.Do(ctx, service, "DELETE", p, nil)
	var e *client.Error
	switch {
	case errors.As(err, &e) && e.StatusCode == 409 && e.Code == "operation_in_progress":
	case err != nil:
		if isNotFound(err) {
			return nil
		}
		return err
	default:
		if err = waitResponse(ctx, c, out); err != nil {
			return err
		}
	}
	for {
		out, err := c.Do(ctx, service, "GET", p, nil)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var v struct{ Status string }
		if err = out.Decode(&v); err != nil {
			return err
		}
		if v.Status != "deleting" {
			return fmt.Errorf("%s was deleted but is still returned by its API as %s; refresh and retry", p[strings.LastIndex(p, "/")+1:], v.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// readOrForget reads p into v for Read. It reports whether v was read; a confirmed 404 removes the
// resource from state.
func readOrForget(ctx context.Context, c *client.Client, project, p, prefix string, v any, state *tfsdk.State, d *diag.Diagnostics) bool {
	err := get(ctx, c, p, prefix, v)
	if err == nil {
		return true
	}
	missing, e := confirmedMissing(ctx, c, project, err)
	if missing {
		state.RemoveResource(ctx)
	} else {
		apiError(d, e)
	}
	return false
}

func isNotFound(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.StatusCode == 404
}

// confirmedMissing tells a missing resource from a hidden or inaccessible project.
func confirmedMissing(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	if !isNotFound(err) {
		return false, err
	}
	if _, parentErr := c.Do(ctx, "orgs", "GET", client.OrgPath(c.OrganizationID, "projects", project), nil); parentErr != nil {
		return false, fmt.Errorf("cannot confirm resource absence because its project is inaccessible: %w", parentErr)
	}
	return true, nil
}

// apiError adds kubernetes-api's own message and extras (pre-check reasons, field issues); an
// access denial keeps the provider's rule of naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	d.AddError("EZGH Cloud API request failed", errorDetail(err))
}
func errorDetail(err error) string {
	detail := err.Error()
	var op *client.OperationError
	if errors.As(err, &op) && op.Message != "" && op.Code != "access_denied" {
		detail += "\n" + op.Message
	}
	var e *client.Error
	if !errors.As(err, &e) || e.Code == "access_denied" {
		return detail
	}
	detail += "\n" + e.Message
	for _, k := range slices.Sorted(maps.Keys(e.Details)) {
		if k == "reasons" { // pre-check refusals, one line each
			var reasons []struct{ Code, Message string }
			if json.Unmarshal(e.Details[k], &reasons) == nil {
				for _, r := range reasons {
					detail += "\n- " + r.Message + " (" + r.Code + ")"
				}
				continue
			}
		}
		detail += "\n" + k + ": " + string(e.Details[k])
	}
	return detail
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

// timeoutContext bounds one operation wait by operation_timeout, else def.
func timeoutContext(ctx context.Context, v types.String, def string, d *diag.Diagnostics) (context.Context, context.CancelFunc) {
	s := v.ValueString()
	if s == "" {
		s = def
	}
	duration, err := time.ParseDuration(s)
	if err != nil || duration <= 0 {
		d.AddError("Invalid operation timeout", "operation_timeout must be a positive duration, for example "+def)
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, duration)
}

// importIDs reads `organization_id/project_id/<parts…>`: each later part has its prefix, or is free
// text when the prefix is "".
func importIDs(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, c *client.Client, attrs, prefixes []string) {
	parts := strings.Split(req.ID, "/")
	ok := len(parts) == 2+len(prefixes) && parts[0] != "" && parts[1] != ""
	for i, prefix := range prefixes {
		ok = ok && strings.HasPrefix(parts[2+i], prefix) && (prefix == "" || prefixedID.MatchString(parts[2+i])) && parts[2+i] != ""
	}
	if !ok {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/"+strings.Join(prefixes, "<ID>/")+"<ID>.")
		return
	}
	if c != nil && parts[0] != c.OrganizationID {
		resp.Diagnostics.AddError("Organization mismatch", "Import organization must match the configured provider.")
		return
	}
	for i, k := range append([]string{"organization_id", "project_id"}, attrs...) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), parts[i])...)
	}
}

func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

func stringMap(ctx context.Context, v types.Map, d *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	if known(v) {
		d.Append(v.ElementsAs(ctx, &out, false)...)
	}
	return out
}
func mapValue(ctx context.Context, v map[string]string) types.Map {
	if v == nil {
		v = map[string]string{}
	}
	out, _ := types.MapValueFrom(ctx, types.StringType, v)
	return out
}
func stringsOf(ctx context.Context, v attr.Value, d *diag.Diagnostics) []string {
	out := []string{}
	switch t := v.(type) {
	case types.List:
		if known(t) {
			d.Append(t.ElementsAs(ctx, &out, false)...)
		}
	case types.Set:
		if known(t) {
			d.Append(t.ElementsAs(ctx, &out, false)...)
		}
	}
	return out
}
func listValue(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	out, _ := types.ListValueFrom(ctx, types.StringType, v)
	return out
}
func setValue(ctx context.Context, v []string) types.Set {
	if v == nil {
		v = []string{}
	}
	out, _ := types.SetValueFrom(ctx, types.StringType, v)
	return out
}

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

// startAsync sends an async create (POST) and saves the assigned identity and operation before
// waiting, so a failed wait leaves the resource in state to refresh, not an orphan. It reports
// whether the operation succeeded; a wait cut short by the timeout is a warning, not an error that
// would taint a resource still being created.
func startAsync(ctx context.Context, c *client.Client, p string, body map[string]any, id, opID *types.String, state *tfsdk.State, model any, noun string, d *diag.Diagnostics) bool {
	out, err := c.Do(ctx, service, "POST", p, body)
	if err != nil {
		apiError(d, err)
		return false
	}
	slug, err := operationResourceID(out.Body)
	if err != nil {
		apiError(d, err)
		return false
	}
	*id, *opID = types.StringValue(slug), operationID(out)
	d.Append(setKnown(ctx, state, model)...)
	if d.HasError() {
		return false
	}
	return finish(ctx, c, out, slug, opID.ValueString(), noun, d)
}

// waitMoving waits, in Read, for the operation a creating or updating resource is in (one whose wait
// an apply cut short), so the next plan sees where it ends rather than diffing against a change still
// running. It reports whether it waited; the caller reads the resource again.
func waitMoving(ctx context.Context, c *client.Client, status string, op, timeout types.String, def string, d *diag.Diagnostics) bool {
	if (status != "creating" && status != "updating") || op.ValueString() == "" {
		return false
	}
	waitCtx, cancel := timeoutContext(ctx, timeout, def, d)
	defer cancel()
	if d.HasError() {
		return false
	}
	if err := c.Wait(waitCtx, service, client.OrgPath(c.OrganizationID, "operations", op.ValueString())); err != nil {
		var opErr *client.OperationError
		if !errors.As(err, &opErr) { // a failed operation leaves the resource failed: the read shows it
			apiError(d, err)
			return false
		}
	}
	return true
}

// replaceFailed plans a failed node group or add-on (a create or change that failed) as a
// replacement: delete it and make it again, which shows the API's refusal if there is one.
func replaceFailed(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var status types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("status"), &status)...)
	if status.ValueString() == "failed" {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("status"))
	}
}

// finish waits for an accepted operation.
func finish(ctx context.Context, c *client.Client, out *client.Response, id, op, noun string, d *diag.Diagnostics) bool {
	err := waitResponse(ctx, c, out)
	if err == nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		d.AddWarning(noun+" still changing", fmt.Sprintf("%s was still changing when the wait ended. The next refresh shows where it got to; operation %s.", id, op))
		return false
	}
	apiError(d, fmt.Errorf("%s, operation %s: %w", id, op, err))
	return false
}
