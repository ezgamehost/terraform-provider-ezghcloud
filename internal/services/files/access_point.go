package files

import (
	"context"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type creationInfo struct {
	OwnerUID    types.Int64  `tfsdk:"owner_uid"`
	OwnerGID    types.Int64  `tfsdk:"owner_gid"`
	Permissions types.String `tfsdk:"permissions"`
}
type rootDirectory struct {
	Path         types.String  `tfsdk:"path"`
	CreationInfo *creationInfo `tfsdk:"creation_info"`
}
type posixUser struct {
	UID           types.Int64 `tfsdk:"uid"`
	GID           types.Int64 `tfsdk:"gid"`
	SecondaryGIDs types.List  `tfsdk:"secondary_gids"`
}
type accessPointModel struct {
	common
	Description   types.String   `tfsdk:"description"`
	FileSystemID  types.String   `tfsdk:"file_system_id"`
	RootDirectory *rootDirectory `tfsdk:"root_directory"`
	PosixUser     *posixUser     `tfsdk:"posix_user"`
}
type accessPointView struct {
	view
	Description   *string
	FileSystem    string
	RootDirectory struct {
		Path         string
		CreationInfo *struct {
			OwnerUID, OwnerGID int64
			Permissions        string
		}
	}
	PosixUser *struct {
		UID, GID      int64
		SecondaryGids []int64
	}
}

type accessPointResource struct{ client *client.Client }

func (*accessPointResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_point"
}

var creationInfoType = map[string]attr.Type{"owner_uid": types.Int64Type, "owner_gid": types.Int64Type, "permissions": types.StringType}

func (*accessPointResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	a := commonAttributes("Access point", "fap_")
	replaceObject := []planmodifier.Object{objectplanmodifier.RequiresReplace()}
	a["description"] = schema.StringAttribute{Optional: true, Description: "Optional description; null clears it."}
	a["file_system_id"] = schema.StringAttribute{Required: true, Description: "File system (fsy_) it exposes. Changing it replaces the access point.", PlanModifiers: replace}
	a["root_directory"] = schema.SingleNestedAttribute{Optional: true, Computed: true, PlanModifiers: replaceObject,
		Default: objectdefault.StaticValue(types.ObjectValueMust(map[string]attr.Type{"path": types.StringType, "creation_info": types.ObjectType{AttrTypes: creationInfoType}},
			map[string]attr.Value{"path": types.StringValue("/"), "creation_info": types.ObjectNull(creationInfoType)})),
		Description: "Folder clients see as the root when they mount the access point. Defaults to the file system's root. Any change replaces the access point.",
		Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("/"), Description: "Absolute, normalized path inside the file system, such as /wordpress/wp-content."},
			"creation_info": schema.SingleNestedAttribute{Optional: true, Description: "Owner and mode to create path with when it doesn't exist. Without it, a missing path fails the create.", Attributes: map[string]schema.Attribute{
				"owner_uid":   schema.Int64Attribute{Required: true, Description: "Owner user ID."},
				"owner_gid":   schema.Int64Attribute{Required: true, Description: "Owner group ID."},
				"permissions": schema.StringAttribute{Required: true, Description: "Octal mode, such as 755."},
			}},
		}}
	a["posix_user"] = schema.SingleNestedAttribute{Optional: true, PlanModifiers: replaceObject,
		Description: "User and groups every request through the access point runs as, whatever the client sends. Without it, the file system's root_squash applies. Any change replaces the access point.",
		Attributes: map[string]schema.Attribute{
			"uid": schema.Int64Attribute{Required: true, Description: "User ID."},
			"gid": schema.Int64Attribute{Required: true, Description: "Group ID."},
			"secondary_gids": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.Int64Type, Default: listdefault.StaticValue(types.ListValueMust(types.Int64Type, []attr.Value{})),
				Description: "Up to 16 more group IDs."},
		}}
	resp.Schema = schema.Schema{Description: "An application's entry into a file system: a folder as its root and, optionally, a fixed POSIX user. Destroying it removes the entry, never the files.", Attributes: a}
}
func (r *accessPointResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configure(req.ProviderData, &resp.Diagnostics, &r.client)
}

func (r *accessPointResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m accessPointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"fileSystem": m.FileSystemID.ValueString(), "name": m.Name.ValueString(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if known(m.Description) {
		body["description"] = m.Description.ValueString()
	}
	if rd := m.RootDirectory; rd != nil {
		root := map[string]any{"path": rd.Path.ValueString()}
		if ci := rd.CreationInfo; ci != nil {
			root["creationInfo"] = map[string]any{"ownerUid": ci.OwnerUID.ValueInt64(), "ownerGid": ci.OwnerGID.ValueInt64(), "permissions": ci.Permissions.ValueString()}
		}
		body["rootDirectory"] = root
	}
	if pu := m.PosixUser; pu != nil {
		gids := []int64{}
		resp.Diagnostics.Append(pu.SecondaryGIDs.ElementsAs(ctx, &gids, false)...)
		body["posixUser"] = map[string]any{"uid": pu.UID.ValueInt64(), "gid": pu.GID.ValueInt64(), "secondaryGids": gids}
	}
	if resp.Diagnostics.HasError() || !create(ctx, r.client, "access-points", &m.common, &m, &resp.State, body, &resp.Diagnostics) {
		return
	}
	var v accessPointView
	if err := read(ctx, r.client, m.ProjectID.ValueString(), "access-points", m.ID.ValueString(), "fap_", &v); err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *accessPointResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m accessPointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	var v accessPointView
	if readState(ctx, r.client, &m.common, "access-points", "fap_", &v, &resp.State, &resp.Diagnostics) {
		m.fromAPI(ctx, v, r.client.OrganizationID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// Update changes name, description and tags, synchronously; everything else replaces.
func (r *accessPointResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m accessPointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueStringPointer(), "tags": tagsOf(ctx, m.Tags, &resp.Diagnostics)}
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.Do(ctx, service, "PATCH", itemPath(r.client, m.ProjectID.ValueString(), "access-points", m.ID.ValueString()), body)
	var v accessPointView
	if err == nil {
		err = out.Decode(&v)
	}
	if err != nil {
		apiError(&resp.Diagnostics, err)
		return
	}
	m.fromAPI(ctx, v, r.client.OrganizationID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *accessPointResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m accessPointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !validateOrg(m.OrganizationID, r.client, &resp.Diagnostics) {
		return
	}
	remove(ctx, r.client, "access-points", &m.common, &resp.Diagnostics)
}
func (r *accessPointResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProject(ctx, req, resp, r.client, "fap_")
}

func (m *accessPointModel) fromAPI(ctx context.Context, v accessPointView, org string) {
	description := m.Description
	m.fill(ctx, v.view, org)
	m.Description = types.StringPointerValue(v.Description)
	if description.ValueString() == "" && known(description) && v.Description == nil {
		m.Description = description // the API shows an empty description as null
	}
	m.FileSystemID = types.StringValue(v.FileSystem)
	m.RootDirectory = &rootDirectory{Path: types.StringValue(v.RootDirectory.Path)}
	if ci := v.RootDirectory.CreationInfo; ci != nil {
		m.RootDirectory.CreationInfo = &creationInfo{OwnerUID: types.Int64Value(ci.OwnerUID), OwnerGID: types.Int64Value(ci.OwnerGID), Permissions: types.StringValue(ci.Permissions)}
	}
	m.PosixUser = nil
	if pu := v.PosixUser; pu != nil {
		if pu.SecondaryGids == nil {
			pu.SecondaryGids = []int64{}
		}
		gids, _ := types.ListValueFrom(ctx, types.Int64Type, pu.SecondaryGids)
		m.PosixUser = &posixUser{UID: types.Int64Value(pu.UID), GID: types.Int64Value(pu.GID), SecondaryGIDs: gids}
	}
}
