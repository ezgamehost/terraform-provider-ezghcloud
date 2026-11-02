package compute

import (
	"context"
	"fmt"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type sshKeyResource struct{ client *client.Client }
type sshKeyModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Name           types.String `tfsdk:"name"`
	PublicKey      types.String `tfsdk:"public_key"`
	Tags           types.Map    `tfsdk:"tags"`
	Fingerprint    types.String `tfsdk:"fingerprint"`
}
type sshKeyView struct {
	ID, Slug, Name, PublicKey, Fingerprint string
	Tags                                   map[string]string
}

func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &sshKeyResource{} }, func() resource.Resource { return &volumeResource{} }, func() resource.Resource { return &instanceResource{} }}
}
func (*sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}
func (*sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "An uploaded SSH public key. Deleting it does not alter copies on existing instances.", Attributes: map[string]schema.Attribute{
		"id": computedID(), "organization_id": organizationAttribute(), "project_id": projectAttribute(), "name": schema.StringAttribute{Required: true},
		"public_key": schema.StringAttribute{Required: true, Description: "One OpenSSH public key line. Changing the key replaces this resource.", PlanModifiers: []planmodifierString{stringplanmodifier.RequiresReplace()}},
		"tags":       tagsAttribute(), "fingerprint": schema.StringAttribute{Computed: true},
	}}
}
func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req, &resp.Diagnostics, &r.client)
}
func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, "compute", "POST", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "ssh-keys"), map[string]any{"name": m.Name.ValueString(), "publicKey": m.PublicKey.ValueString(), "tags": tags})
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	var v sshKeyView
	if err = out.Decode(&v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	v, err := readSSHKey(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if missing {
			resp.State.RemoveResource(ctx)
			return
		}
		apiError(&resp.Diagnostics, e)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	tags, d := stringMap(ctx, m.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, "compute", "PATCH", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "ssh-keys", m.ID.ValueString()), map[string]any{"name": m.Name.ValueString(), "tags": tags})
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	var v sshKeyView
	if err = out.Decode(&v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	_, err := r.client.Do(ctx, "compute", "DELETE", client.ProjectPath(r.client.OrganizationID, m.ProjectID.ValueString(), "ssh-keys", m.ID.ValueString()), nil)
	if err != nil {
		missing, e := confirmedMissing(ctx, r.client, m.ProjectID.ValueString(), err)
		if !missing {
			apiError(&resp.Diagnostics, e)
		}
	}
}
func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client)
}
func (m *sshKeyModel) fromAPI(ctx context.Context, v sshKeyView, org string) {
	m.ID = types.StringValue(v.Slug)
	m.OrganizationID = types.StringValue(org)
	m.Name = types.StringValue(v.Name)
	if m.PublicKey.IsNull() || m.PublicKey.IsUnknown() || !samePublicKey(m.PublicKey.ValueString(), v.PublicKey) {
		m.PublicKey = types.StringValue(v.PublicKey)
	}
	m.Fingerprint = types.StringValue(v.Fingerprint)
	m.Tags = mapValue(ctx, v.Tags)
}
func readSSHKey(ctx context.Context, c *client.Client, p, id string) (sshKeyView, error) {
	var v sshKeyView
	out, err := c.Do(ctx, "compute", "GET", client.ProjectPath(c.OrganizationID, p, "ssh-keys", id), nil)
	if err != nil {
		return v, err
	}
	err = out.Decode(&v)
	if err == nil && v.Slug == "" {
		err = fmt.Errorf("SSH key response omitted canonical identifier")
	}
	return v, err
}
func importProject(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, c *client.Client) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid import identifier", "Expected organization_id/project_id/resource_id.")
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

func samePublicKey(a, b string) bool {
	aa, bb := strings.Fields(a), strings.Fields(b)
	return len(aa) >= 2 && len(bb) >= 2 && aa[0] == bb[0] && aa[1] == bb[1]
}
