package waf

import (
	"context"
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

// lbService is lb-api, which owns a load balancer's wafId.
const lbService = "lb"

type loadBalancerWAFModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	WafID          types.String `tfsdk:"waf_id"`
}

type loadBalancerWAFResource struct{ client *client.Client }

func (*loadBalancerWAFResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_waf"
}
func (*loadBalancerWAFResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Attaches a WAF to a load balancer, where it inspects requests to the http and https listeners. A load balancer has at most one WAF; destroying this detaches it.", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true, Description: "The load balancer's ID (lbr_).", PlanModifiers: keepString},
		"organization_id":  schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: keepString},
		"project_id":       schema.StringAttribute{Required: true, Description: "Project of the load balancer and the WAF. Changing it replaces the attachment.", PlanModifiers: replace},
		"load_balancer_id": schema.StringAttribute{Required: true, Description: "Load balancer (lbr_). Changing it replaces the attachment.", PlanModifiers: replace},
		"waf_id":           schema.StringAttribute{Required: true, Description: "WAF (waf_) in the same project. Changing it switches the load balancer to the other WAF in place."},
	}}
}
func (r *loadBalancerWAFResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

type loadBalancerView struct {
	Slug  string
	WafID *string
}

func (r *loadBalancerWAFResource) path(m *loadBalancerWAFModel) string {
	return client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "load-balancers", m.LoadBalancerID.ValueString())
}

// set PATCHes the load balancer's wafId (nil detaches) and answers the load balancer.
func (r *loadBalancerWAFResource) set(ctx context.Context, m *loadBalancerWAFModel, waf *string) (loadBalancerView, error) {
	var v loadBalancerView
	out, err := r.client.DoWithHeaders(ctx, lbService, "PATCH", r.path(m), map[string]any{"wafId": waf}, http.Header{"Idempotency-Key": {idempotencyKey()}})
	if err == nil {
		err = out.Decode(&v)
	}
	return v, err
}

func (r *loadBalancerWAFResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m loadBalancerWAFModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.apply(ctx, &m, resp.State.Set, &resp.Diagnostics)
}
func (r *loadBalancerWAFResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m loadBalancerWAFModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	r.apply(ctx, &m, resp.State.Set, &resp.Diagnostics)
}
func (r *loadBalancerWAFResource) apply(ctx context.Context, m *loadBalancerWAFModel, save func(context.Context, any) diag.Diagnostics, d *diag.Diagnostics) {
	waf := m.WafID.ValueString()
	v, err := r.set(ctx, m, &waf)
	if err == nil && (v.WafID == nil || *v.WafID != waf) {
		err = errors.New("the load balancer's answer doesn't show the WAF attached")
	}
	if err != nil {
		apiError(d, err)
		return
	}
	m.ID, m.OrganizationID = m.LoadBalancerID, types.StringValue(r.client.OrganizationID)
	d.Append(save(ctx, m)...)
}

// Read drops the attachment when the load balancer is gone or has no WAF, and shows another WAF
// attached outside Terraform as a change to undo.
func (r *loadBalancerWAFResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m loadBalancerWAFModel
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
	if err != nil || v.WafID == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.ID, m.OrganizationID, m.WafID = m.LoadBalancerID, types.StringValue(r.client.OrganizationID), types.StringValue(*v.WafID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *loadBalancerWAFResource) read(ctx context.Context, m *loadBalancerWAFModel) (loadBalancerView, error) {
	var v loadBalancerView
	out, err := r.client.Do(ctx, lbService, "GET", r.path(m), nil)
	if err == nil {
		err = out.Decode(&v)
	}
	return v, err
}

// Delete detaches the WAF, unless the load balancer is gone or has another WAF by now.
func (r *loadBalancerWAFResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m loadBalancerWAFModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := r.read(ctx, &m)
	if err == nil && v.WafID != nil && *v.WafID == m.WafID.ValueString() {
		_, err = r.set(ctx, &m, nil)
	}
	if err == nil {
		return
	}
	if missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err); !missing {
		apiError(&resp.Diagnostics, e)
	}
}
func (r *loadBalancerWAFResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "load_balancer_id", "lbr_")
}
