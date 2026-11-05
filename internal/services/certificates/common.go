// Package certificates implements EZGH Web Certificates resources: domain authorizations and
// certificates.
package certificates

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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const service = "certificates"

var (
	prefixedID = regexp.MustCompile(`^[a-z]+_[a-z0-9]{12}$`)
	uuid       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &authorizationResource{} }, func() resource.Resource { return &certificateResource{} }}
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

func missingProvider(d *diag.Diagnostics, c *client.Client) bool {
	if c == nil {
		d.AddError("Provider not configured", "Configure the EZGH Cloud provider before using resources.")
		return true
	}
	return false
}

// apiError adds the API's own message and extras; an access denial keeps the provider's rule of
// naming only the permission.
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

func isStatus(err error, status int) bool {
	var e *client.Error
	return errors.As(err, &e) && e.StatusCode == status
}

func tagsOf(ctx context.Context, v types.Map, d *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	d.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// timeoutsAttribute is the optional timeouts block; each value is a Go duration such as "45m".
func timeoutsAttribute() schema.Attribute {
	return schema.SingleNestedAttribute{Optional: true, Description: "Operation timeouts as durations such as 45m. Both default to 30m.", Attributes: map[string]schema.Attribute{
		"create": schema.StringAttribute{Optional: true, Description: "How long to wait for issuance. Default 30m."},
		"delete": schema.StringAttribute{Optional: true, Description: "How long to wait for deletion. Default 30m."},
	}}
}

var timeoutsTypes = map[string]attr.Type{"create": types.StringType, "delete": types.StringType}

func timeoutOf(o types.Object, key string, d *diag.Diagnostics) time.Duration {
	const def = 30 * time.Minute
	if !known(o) {
		return def
	}
	s, _ := o.Attributes()[key].(types.String)
	if !known(s) {
		return def
	}
	v, err := time.ParseDuration(s.ValueString())
	if err != nil || v <= 0 {
		d.AddError("Invalid timeout", fmt.Sprintf("timeouts.%s must be a positive duration such as 45m.", key))
		return def
	}
	return v
}

// operationResourceID is the slug that ends the operation target's resource name.
func operationResourceID(body []byte, prefix string) (string, error) {
	var v struct{ Target struct{ ResourceName string } }
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("invalid operation response")
	}
	rn := v.Target.ResourceName
	id := rn[strings.LastIndexAny(rn, ":/")+1:]
	if !strings.HasPrefix(id, prefix) || !prefixedID.MatchString(id) {
		return "", fmt.Errorf("operation response omitted the assigned resource identity")
	}
	return id, nil
}
