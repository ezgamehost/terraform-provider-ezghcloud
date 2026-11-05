package certificates

import (
	"context"
	"fmt"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type certificateModel struct {
	ID                     types.String `tfsdk:"id"`
	ProjectID              types.String `tfsdk:"project_id"`
	Name                   types.String `tfsdk:"name"`
	DNSNames               types.Set    `tfsdk:"dns_names"`
	DomainAuthorizationIDs types.Set    `tfsdk:"domain_authorization_ids"`
	KeyAlgorithm           types.String `tfsdk:"key_algorithm"`
	RenewalEnabled         types.Bool   `tfsdk:"renewal_enabled"`
	Tags                   types.Map    `tfsdk:"tags"`
	Status                 types.String `tfsdk:"status"`
	ActiveVersion          types.String `tfsdk:"active_version"`
	Timeouts               types.Object `tfsdk:"timeouts"`
}

type certificateView struct {
	Slug, Name, KeyAlgorithm, Status string
	DNSNames                         []string
	DomainAuthorizationIDs           []string
	RenewalEnabled                   bool
	Tags                             map[string]string
	ActiveVersionID                  *string
}

type certificateResource struct{ client *client.Client }

func (*certificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}
func (*certificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceSet := []planmodifier.Set{setplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "A public TLS certificate. Creating it waits until it is issued; the domain authorizations must be verified by then.", Attributes: map[string]schema.Attribute{
		"id":                       schema.StringAttribute{Computed: true, Description: "Certificate ID (crt_).", PlanModifiers: keep},
		"project_id":               schema.StringAttribute{Required: true, Description: "Project identifier. Changing it replaces the certificate.", PlanModifiers: replace},
		"name":                     schema.StringAttribute{Required: true, Description: "Name, 1 to 100 characters."},
		"dns_names":                schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Names the certificate covers; each needs a domain authorization. Changing them replaces the certificate.", PlanModifiers: replaceSet},
		"domain_authorization_ids": schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Domain authorizations (dau_) that cover dns_names. Changing them replaces the certificate.", PlanModifiers: replaceSet},
		"key_algorithm":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("ecdsa-p256"), Description: "ecdsa-p256 or rsa-2048. Defaults to ecdsa-p256. Changing it replaces the certificate.", PlanModifiers: replace},
		"renewal_enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Renew the certificate automatically. Defaults to true."},
		"tags":                     schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."},
		"status":                   schema.StringAttribute{Computed: true, Description: "Issuance status: pending, issued, failed or deleting."},
		"active_version":           schema.StringAttribute{Computed: true, Description: "ID of the version being served, or null before the first issuance."},
		"timeouts":                 timeoutsAttribute(),
	}}
}
func (r *certificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *certificateResource) path(project string, id ...string) string {
	return client.ProjectPath(r.client.OrganizationID, project, "certificates", id...)
}

func (r *certificateResource) get(ctx context.Context, project, id string) (certificateView, error) {
	var v certificateView
	out, err := r.client.Do(ctx, service, "GET", r.path(project, id), nil)
	if err == nil {
		err = out.Decode(&v)
	}
	return v, err
}

// authorizationUUIDs maps dau_ IDs to the UUIDs the certificates API takes in domainAuthorizationIds.
func (r *certificateResource) authorizationUUIDs(ctx context.Context, ids []string) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		if uuid.MatchString(id) {
			out[i] = id
			continue
		}
		resp, err := r.client.Do(ctx, service, "GET", client.OrgPath(r.client.OrganizationID, "domain-authorizations", id), nil)
		var v authorizationView
		if err == nil {
			err = resp.Decode(&v)
		}
		if err != nil || v.ID == "" {
			return nil, fmt.Errorf("domain authorization %s: %w", id, errOrEmpty(err))
		}
		out[i] = v.ID
	}
	return out, nil
}
func errOrEmpty(err error) error {
	if err == nil {
		return fmt.Errorf("response omitted the authorization ID")
	}
	return err
}

// authorizationSlugs is the reverse of authorizationUUIDs, for reading state after an import.
func (r *certificateResource) authorizationSlugs(ctx context.Context, ids []string) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		resp, err := r.client.Do(ctx, service, "GET", client.OrgPath(r.client.OrganizationID, "domain-authorizations", id), nil)
		var v authorizationView
		if err == nil {
			err = resp.Decode(&v)
		}
		if err != nil || v.Slug == "" {
			return nil, fmt.Errorf("domain authorization %s: %w", id, errOrEmpty(err))
		}
		out[i] = v.Slug
	}
	return out, nil
}

// fromAPI fills m. The authorization IDs stay as configured (they can't change in place); only an
// import, which has none, looks them up.
func (r *certificateResource) fromAPI(ctx context.Context, m *certificateModel, v certificateView, diags *diag.Diagnostics) {
	m.ID, m.Name, m.KeyAlgorithm, m.RenewalEnabled, m.Status = types.StringValue(v.Slug), types.StringValue(v.Name), types.StringValue(v.KeyAlgorithm), types.BoolValue(v.RenewalEnabled), types.StringValue(v.Status)
	m.ActiveVersion = types.StringPointerValue(v.ActiveVersionID)
	if v.Tags == nil {
		v.Tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, v.Tags)
	m.DNSNames, _ = types.SetValueFrom(ctx, types.StringType, append([]string{}, v.DNSNames...))
	if !known(m.DomainAuthorizationIDs) {
		slugs, err := r.authorizationSlugs(ctx, v.DomainAuthorizationIDs)
		if err != nil {
			apiError(diags, err)
			return
		}
		m.DomainAuthorizationIDs, _ = types.SetValueFrom(ctx, types.StringType, slugs)
	}
}

func (r *certificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m certificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	wait := timeoutOf(m.Timeouts, "create", &resp.Diagnostics)
	var names, auths []string
	resp.Diagnostics.Append(m.DNSNames.ElementsAs(ctx, &names, false)...)
	resp.Diagnostics.Append(m.DomainAuthorizationIDs.ElementsAs(ctx, &auths, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	uuids, err := r.authorizationUUIDs(ctx, auths)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	out, err := r.client.Do(ctx, service, "POST", r.path(m.ProjectID.ValueString()), map[string]any{"name": m.Name.ValueString(), "dnsNames": names, "domainAuthorizationIds": uuids,
		"keyAlgorithm": m.KeyAlgorithm.ValueString(), "renewalEnabled": m.RenewalEnabled.ValueBool(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)})
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	id, err := operationResourceID(out.Body, "crt_")
	if err == nil && out.StatusCode != 202 {
		err = fmt.Errorf("the certificate request was not accepted as an operation")
	}
	if err == nil && out.Header.Get("Location") == "" {
		err = fmt.Errorf("accepted operation omitted Location; resource identity retained")
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	// Saved before the wait, so a failed or timed-out issuance leaves a tainted certificate to replace.
	m.ID, m.Status, m.ActiveVersion = types.StringValue(id), types.StringValue("pending"), types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = r.client.Wait(ctx, service, out.Header.Get("Location")); err != nil {
		apiError(&resp.Diagnostics, fmt.Errorf("%s: %w", id, err))
		return
	}
	v, err := r.get(ctx, m.ProjectID.ValueString(), id)
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.fromAPI(ctx, &m, v, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *certificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	v, err := r.get(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if isStatus(err, 404) {
		// A hidden project also answers 404; only an accessible project proves the certificate is gone.
		if _, perr := r.client.Do(ctx, "orgs", "GET", client.OrgPath(r.client.OrganizationID, "projects", m.ProjectID.ValueString()), nil); perr != nil {
			apiError(&resp.Diagnostics, fmt.Errorf("cannot confirm the certificate is gone because its project is inaccessible: %w", perr))
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.fromAPI(ctx, &m, v, &resp.Diagnostics)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// Update changes the name, tags and renewal, which the API changes in place.
func (r *certificateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m certificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", r.path(m.ProjectID.ValueString(), m.ID.ValueString()),
		map[string]any{"name": m.Name.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics), "renewalEnabled": m.RenewalEnabled.ValueBool()})
	var v certificateView
	if err == nil {
		err = out.Decode(&v)
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	r.fromAPI(ctx, &m, v, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *certificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || missingProvider(&resp.Diagnostics, r.client) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(m.Timeouts, "delete", &resp.Diagnostics))
	defer cancel()
	out, err := r.client.Do(ctx, service, "DELETE", r.path(m.ProjectID.ValueString(), m.ID.ValueString()), nil)
	if err == nil && out.StatusCode == 202 {
		err = r.client.Wait(ctx, service, out.Header.Get("Location"))
	}
	if err != nil && !isStatus(err, 404) {
		apiError(&resp.Diagnostics, err)
	}
}

// ImportState takes project_id/crt_<12 characters>.
func (r *certificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || !strings.HasPrefix(id, "crt_") || !prefixedID.MatchString(id) {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected project_id/crt_<12-character ID>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
