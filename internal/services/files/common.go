// Package files implements EZGH Files resources: file systems, their mount targets and access points.
package files

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const service = "files"

// operationTimeout bounds each wait on a create or delete; these take seconds to minutes.
const operationTimeout = time.Hour

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &fileSystemResource{} },
		func() resource.Resource { return &mountTargetResource{} }, func() resource.Resource { return &accessPointResource{} }}
}

// common are the attributes every Files resource has.
type common struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	ResourceName   types.String `tfsdk:"resource_name"`
	Name           types.String `tfsdk:"name"`
	Tags           types.Map    `tfsdk:"tags"`
	Status         types.String `tfsdk:"status"`
}

// view is what every Files resource shows.
type view struct {
	Slug, ResourceName, Name, Status string
	Tags                             map[string]string
}

func (c *common) fill(ctx context.Context, v view, org string) {
	c.ID = types.StringValue(v.Slug)
	c.OrganizationID = types.StringValue(org)
	c.ResourceName = types.StringValue(v.ResourceName)
	c.Name = types.StringValue(v.Name)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	c.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	c.Status = types.StringValue(v.Status)
}

var (
	keepString = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace    = []planmodifier.String{stringplanmodifier.RequiresReplace()}
)

// commonAttributes are common's schema; noun names the resource in descriptions.
func commonAttributes(noun, prefix string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: noun + " ID (" + prefix + ").", PlanModifiers: keepString},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the " + strings.ToLower(noun) + ".", PlanModifiers: replace},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name.", PlanModifiers: keepString},
		"name":            schema.StringAttribute{Required: true, Description: "Name, 1 to 100 characters."},
		"tags":            schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"status":          schema.StringAttribute{Computed: true, Description: "What the " + strings.ToLower(noun) + " is doing: creating, available, deleting or failed."},
	}
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

// itemPath is a resource's path; collection is file-systems, mount-targets or access-points.
func itemPath(c *client.Client, project, collection string, id ...string) string {
	return client.ProjectPath(c.OrganizationID, project, collection, id...)
}

// read GETs one resource into v and checks its slug has prefix.
func read(ctx context.Context, c *client.Client, project, collection, id, prefix string, v any) error {
	out, err := c.Do(ctx, service, "GET", itemPath(c, project, collection, id), nil)
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

// create POSTs body, saves the assigned identity to state before waiting (so a failed wait leaves a
// tainted resource to replace, not an orphan), and waits for the operation. It reports success.
func create(ctx context.Context, c *client.Client, collection string, m *common, model any, state *tfsdk.State, body map[string]any, diags *diag.Diagnostics) bool {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	out, err := c.Do(ctx, service, "POST", itemPath(c, m.ProjectID.ValueString(), collection), body)
	if err != nil {
		apiError(diags, err)
		return false
	}
	id, err := operationResourceID(out.Body)
	if err != nil {
		apiError(diags, err)
		return false
	}
	m.ID, m.OrganizationID, m.Status = types.StringValue(id), types.StringValue(c.OrganizationID), types.StringValue("creating")
	diags.Append(setKnown(ctx, state, model)...)
	if diags.HasError() {
		return false
	}
	if err = waitResponse(ctx, c, out); err != nil {
		apiError(diags, fmt.Errorf("%s: %w", id, err))
		return false
	}
	return true
}

// remove DELETEs a resource, waits for its operation, and checks it's gone.
func remove(ctx context.Context, c *client.Client, collection string, m *common, diags *diag.Diagnostics) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	p := itemPath(c, m.ProjectID.ValueString(), collection, m.ID.ValueString())
	out, err := c.Do(ctx, service, "DELETE", p, nil)
	var e *client.Error
	if errors.As(err, &e) && e.StatusCode == 409 && (e.Code == "operation_in_progress" || strings.HasSuffix(e.Code, "_busy")) {
		err = waitDeleting(ctx, c, p, err)
	} else if err == nil {
		err = waitResponse(ctx, c, out)
		if err == nil {
			_, err = c.Do(ctx, service, "GET", p, nil)
			if err == nil {
				diags.AddError("Deletion incomplete", m.ID.ValueString()+" was deleted but is still returned by its API. Refresh and retry.")
				return
			}
		}
	}
	if missing, e := confirmedMissing(ctx, c, m.ProjectID.ValueString(), err); !missing {
		apiError(diags, e)
	}
}

// waitDeleting waits out a delete already in progress (an earlier apply's that was cut short): it
// returns the GET's error once the resource is gone, or busy when it isn't being deleted.
func waitDeleting(ctx context.Context, c *client.Client, p string, busy error) error {
	for {
		out, err := c.Do(ctx, service, "GET", p, nil)
		if err != nil {
			return err
		}
		var v struct{ Status string }
		if err = out.Decode(&v); err != nil {
			return err
		}
		if v.Status != "deleting" {
			return busy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// readState refreshes a resource in Read, removing it from state once it's confirmed gone. It
// reports whether v was read.
func readState(ctx context.Context, c *client.Client, m *common, collection, prefix string, v any, state *tfsdk.State, diags *diag.Diagnostics) bool {
	err := read(ctx, c, m.ProjectID.ValueString(), collection, m.ID.ValueString(), prefix, v)
	if err == nil {
		return true
	}
	missing, e := confirmedMissing(ctx, c, m.ProjectID.ValueString(), err)
	if missing {
		state.RemoveResource(ctx)
	} else {
		apiError(diags, e)
	}
	return false
}

func tagsOf(ctx context.Context, v types.Map, d *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	d.Append(v.ElementsAs(ctx, &out, false)...)
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

// apiError adds files-api's own message and extras (field issues); an access denial keeps the
// provider's rule of naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	detail := err.Error()
	var op *client.OperationError
	if errors.As(err, &op) && op.Message != "" && op.Code != "access_denied" {
		detail += "\n" + op.Message
	}
	var e *client.Error
	if errors.As(err, &e) && e.Code != "access_denied" {
		detail += "\n" + e.Message
		for _, k := range slices.Sorted(maps.Keys(e.Details)) {
			detail += "\n" + k + ": " + string(e.Details[k])
		}
	}
	d.AddError("EZGH Cloud API request failed", detail)
}

// A parent read distinguishes a missing resource from a hidden/inaccessible project.
func confirmedMissing(ctx context.Context, c *client.Client, project string, err error) (bool, error) {
	var e *client.Error
	if !errors.As(err, &e) || e.StatusCode != 404 {
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

// importProject takes organization_id/project_id/<prefix><12 characters>.
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

func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &lookup{kind: "file_system"} }, func() datasource.DataSource { return &lookup{kind: "mount_target"} }}
}
