// Package hostedqueues implements EZGH Hosted Queues resources: managed Kafka and RabbitMQ
// clusters, their users and the broker objects managed through EZGH. A resource touches only its
// own object: destroying one deletes that object alone, and an object no configuration names is
// never read, changed or deleted.
package hostedqueues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const service = "hostedqueues"

var prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)

// Preview is whether Hosted Queues resources are served: only with EZGHCLOUD_PREVIEW_HOSTED_QUEUES=1
// (the tests set it) until Hosted Queues launch, so releases and their Registry docs leave them out.
// At launch, drop it, add the service to the provider's endpoints description and run make docs.
func Preview() bool { return os.Getenv("EZGHCLOUD_PREVIEW_HOSTED_QUEUES") == "1" }

func Resources() []func() resource.Resource {
	if !Preview() {
		return nil
	}
	return []func() resource.Resource{
		func() resource.Resource { return &clusterResource{} },
		func() resource.Resource { return &userResource{} },
		func() resource.Resource { return newTopic() },
		func() resource.Resource { return newVhost() },
		func() resource.Resource { return newQueue() },
		func() resource.Resource { return newExchange() },
		func() resource.Resource { return newBinding() },
		func() resource.Resource { return newPolicy() },
	}
}
func DataSources() []func() datasource.DataSource {
	if !Preview() {
		return nil
	}
	return []func() datasource.DataSource{func() datasource.DataSource { return &lookup{} }}
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

// send makes one async call and waits for its operation. accepted, when set, runs once the API has
// taken the call and before the wait.
func send(ctx context.Context, c *client.Client, method, p string, body any, accepted func(*client.Response) bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	out, err := c.Do(ctx, service, method, p, body)
	if err != nil {
		return err
	}
	if accepted != nil && !accepted(out) {
		return fmt.Errorf("the change was accepted but couldn't be saved to state; import it")
	}
	if err = waitResponse(ctx, c, out); err != nil {
		return fmt.Errorf("operation %s: %w", operationID(out).ValueString(), err)
	}
	return nil
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
func stringList(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	out, _ := types.ListValueFrom(ctx, types.StringType, v)
	return out
}
func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

func setString(body map[string]any, key string, v types.String) {
	if known(v) {
		body[key] = v.ValueString()
	}
}
func setInt(body map[string]any, key string, v types.Int64) {
	if known(v) {
		body[key] = v.ValueInt64()
	}
}
func setBool(body map[string]any, key string, v types.Bool) {
	if known(v) {
		body[key] = v.ValueBool()
	}
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

// apiError adds hostedqueues-api's own message and extras (field issues); an access denial keeps the provider's rule of naming only the permission.
func apiError(d *diag.Diagnostics, err error) {
	detail := err.Error()
	var e *client.Error
	if errors.As(err, &e) && e.Code != "access_denied" {
		detail += "\n" + e.Message
		for _, k := range slices.Sorted(maps.Keys(e.Details)) {
			detail += "\n" + k + ": " + string(e.Details[k])
		}
	}
	var o *client.OperationError
	if errors.As(err, &o) && o.Message != "" {
		detail += "\n" + o.Message
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

// deletePoll is how often a delete already in progress is re-read.
var deletePoll = 2 * time.Second

// waitDeleting waits out a delete an earlier apply started (DELETE answered 409 operation_in_progress).
// It returns the GET's error once the cluster is gone, or busy when it isn't deleting.
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
		case <-time.After(deletePoll):
		}
	}
}

func deleteInProgress(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.StatusCode == 409 && e.Code == "operation_in_progress"
}
