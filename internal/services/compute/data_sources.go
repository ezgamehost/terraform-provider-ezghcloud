package compute

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type lookupDataSource struct {
	kind   string
	client *client.Client
}

func DataSources() []func() datasource.DataSource {
	out := []func() datasource.DataSource{}
	for _, kind := range []string{"ssh_key", "volume", "instance", "image", "instance_type", "volume_type"} {
		k := kind
		out = append(out, func() datasource.DataSource { return &lookupDataSource{kind: k} })
	}
	return out
}
func (r *lookupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind
}
func (r *lookupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{"id": schema.StringAttribute{Required: true, Description: "Canonical prefixed resource identifier."}, "organization_id": schema.StringAttribute{Computed: true}, "name": schema.StringAttribute{Computed: true}}
	if r.kind == "instance_type" || r.kind == "volume_type" {
		attrs["id"] = schema.StringAttribute{Computed: true}
		attrs["name"] = schema.StringAttribute{Required: true, Description: "Exact catalog type name."}
	} else {
		attrs["project_id"] = schema.StringAttribute{Required: true}
	}
	switch r.kind {
	case "ssh_key":
		attrs["public_key"] = schema.StringAttribute{Computed: true}
		attrs["fingerprint"] = schema.StringAttribute{Computed: true}
		attrs["tags"] = schema.MapAttribute{Computed: true, ElementType: types.StringType}
	case "volume":
		for _, k := range []string{"volume_type", "size_bytes", "status"} {
			attrs[k] = schema.StringAttribute{Computed: true}
		}
		attrs["iops"] = schema.Int64Attribute{Computed: true}
		attrs["throughput_mbps"] = schema.Int64Attribute{Computed: true}
		attrs["deletion_protection"] = schema.BoolAttribute{Computed: true}
		attrs["tags"] = schema.MapAttribute{Computed: true, ElementType: types.StringType}
	case "instance":
		for _, k := range []string{"instance_type", "status", "image_id", "root_volume_id", "bot_id"} {
			attrs[k] = schema.StringAttribute{Computed: true}
		}
		attrs["network_interface_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType}
		attrs["volume_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType}
		attrs["tags"] = schema.MapAttribute{Computed: true, ElementType: types.StringType}
	case "image":
		attrs["id"] = schema.StringAttribute{Computed: true}
		attrs["ref"] = schema.StringAttribute{Required: true, Description: "An image alias or exact img_ ID."}
		for _, k := range []string{"architecture", "sha256", "min_disk_bytes", "default_user", "boot_mode"} {
			attrs[k] = schema.StringAttribute{Computed: true}
		}
	case "instance_type":
		for _, k := range []string{"architecture", "family", "status"} {
			attrs[k] = schema.StringAttribute{Computed: true}
		}
		attrs["vcpus"] = schema.Int64Attribute{Computed: true}
		attrs["memory_mib"] = schema.Int64Attribute{Computed: true}
	case "volume_type":
		attrs["display_name"] = schema.StringAttribute{Computed: true}
		attrs["status"] = schema.StringAttribute{Computed: true}
		attrs["baseline_iops"] = schema.Int64Attribute{Computed: true}
		attrs["baseline_throughput_mbps"] = schema.Int64Attribute{Computed: true}
		attrs["minimum_size_gib"] = schema.Int64Attribute{Computed: true}
		attrs["maximum_size_gib"] = schema.Int64Attribute{Computed: true}
	}
	resp.Schema = schema.Schema{Description: "Read an existing EZGH Cloud " + r.kind + ".", Attributes: attrs}
}
func (r *lookupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "Expected an EZGH Cloud API client.")
		return
	}
	r.client = c
}
func (r *lookupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the EZGH Cloud provider.")
		return
	}
	var id, project, name, ref types.String
	if r.kind == "instance_type" || r.kind == "volume_type" {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	} else {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &project)...)
		if r.kind == "image" {
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ref"), &ref)...)
		} else {
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.State.Raw = req.Config.Raw
	set := func(k string, v any) {
		if v == nil {
			switch k {
			case "root_volume_id", "bot_id", "image_id":
				v = types.StringNull()
			default:
				resp.Diagnostics.AddError("Incomplete API response", "The API omitted required lookup field "+k+".")
				return
			}
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
	set("organization_id", r.client.OrganizationID)
	var obj map[string]any
	service, collection := lookupEndpoint(r.kind)
	if r.kind == "volume_type" {
		out, err := r.client.Do(ctx, service, "GET", client.OrgPath(r.client.OrganizationID, collection), nil)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		var page struct{ VolumeTypes []map[string]any }
		if err = out.Decode(&page); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		for _, v := range page.VolumeTypes {
			if v["name"] == name.ValueString() {
				if obj != nil {
					apiError(&resp.Diagnostics, fmt.Errorf("catalog contains duplicate volume type names"))
					return
				}
				obj = v
			}
		}
		if obj == nil {
			apiError(&resp.Diagnostics, fmt.Errorf("volume type %q not found", name.ValueString()))
			return
		}
	} else {
		p := client.ProjectPath(r.client.OrganizationID, project.ValueString(), collection, id.ValueString())
		if r.kind == "instance_type" {
			p = client.OrgPath(r.client.OrganizationID, collection, name.ValueString())
		}
		if r.kind == "image" {
			p = client.ProjectPath(r.client.OrganizationID, project.ValueString(), "image-resolution") + "?ref=" + url.QueryEscape(ref.ValueString())
		}
		out, err := r.client.Do(ctx, service, "GET", p, nil)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		if err = out.Decode(&obj); err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
	}
	if r.kind == "instance_type" || r.kind == "volume_type" {
		set("id", name)
	} else {
		canonical, err := canonicalID(obj)
		if err != nil {
			apiError(&resp.Diagnostics, err)
			return
		}
		set("id", canonical)
		set("name", obj["name"])
	}
	switch r.kind {
	case "ssh_key":
		set("public_key", obj["publicKey"])
		set("fingerprint", obj["fingerprint"])
		set("tags", mapValue(ctx, toStringMap(obj["tags"])))
	case "volume":
		for k, wire := range map[string]string{"volume_type": "volumeType", "size_bytes": "sizeBytes", "status": "status", "iops": "iops", "throughput_mbps": "throughputMBps", "deletion_protection": "deletionProtection"} {
			set(k, obj[wire])
		}
		set("tags", mapValue(ctx, toStringMap(obj["tags"])))
	case "instance":
		for k, wire := range map[string]string{"instance_type": "instanceType", "status": "status", "root_volume_id": "rootVolumeId"} {
			set(k, obj[wire])
		}
		image, _ := obj["image"].(map[string]any)
		set("image_id", image["id"])
		var bot any
		if b, ok := obj["bot"].(map[string]any); ok {
			bot = b["id"]
		}
		set("bot_id", bot)
		set("network_interface_ids", stringList(obj["networkInterfaceIds"]))
		set("volume_ids", stringList(obj["volumeIds"]))
		set("tags", mapValue(ctx, toStringMap(obj["tags"])))
	case "image":
		set("ref", ref)
		for k, wire := range map[string]string{"architecture": "architecture", "sha256": "sha256", "min_disk_bytes": "minDiskBytes", "default_user": "defaultUser", "boot_mode": "bootMode"} {
			set(k, obj[wire])
		}
	case "instance_type":
		for k, wire := range map[string]string{"architecture": "architecture", "family": "family", "status": "status", "vcpus": "vcpus", "memory_mib": "memoryMiB"} {
			set(k, obj[wire])
		}
	case "volume_type":
		set("display_name", obj["displayName"])
		set("status", obj["status"])
		baseline, _ := obj["baseline"].(map[string]any)
		size, _ := obj["sizeGiB"].(map[string]any)
		set("baseline_iops", baseline["iops"])
		set("baseline_throughput_mbps", baseline["throughputMBps"])
		set("minimum_size_gib", size["min"])
		set("maximum_size_gib", size["max"])
	}
}
func lookupEndpoint(kind string) (string, string) {
	switch kind {
	case "ssh_key":
		return "compute", "ssh-keys"
	case "instance":
		return "compute", "instances"
	case "volume":
		return "blockstore", "volumes"
	case "image":
		return "images", "image-resolution"
	case "instance_type":
		return "compute", "instance-types"
	default:
		return "blockstore", "volume-types"
	}
}
func toStringMap(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}
func stringList(v any) []string {
	out := []string{}
	if a, ok := v.([]any); ok {
		for _, v := range a {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
