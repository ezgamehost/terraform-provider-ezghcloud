package network

import (
	"context"
	"fmt"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type lookup struct {
	client *client.Client
	kind   string
}

func (d *lookup) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind
}
func (d *lookup) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := map[string]schema.Attribute{
		"id":              schema.StringAttribute{Required: true, Description: "Canonical prefixed resource ID."},
		"project_id":      schema.StringAttribute{Required: true, Description: "Project containing the resource."},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization containing the resource."},
		"name":            schema.StringAttribute{Computed: true, Description: "Resource name."},
		"description":     schema.StringAttribute{Computed: true, Description: "Resource description."},
		"tags":            schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Customer resource tags."},
		"resource_name":   schema.StringAttribute{Computed: true, Description: "IAM resource name."},
		"status":          schema.StringAttribute{Computed: true, Description: "Service status."},
	}
	if d.kind != "firewall" {
		a["ipv4_cidr"] = schema.StringAttribute{Computed: true, Description: "IPv4 address space."}
		a["ipv6_cidr"] = schema.StringAttribute{Computed: true, Description: "Service-assigned IPv6 address space."}
	}
	switch d.kind {
	case "vpc":
		a["default_firewall_id"] = schema.StringAttribute{Computed: true, Description: "Default firewall ID."}
	case "subnet":
		a["vpc_id"] = schema.StringAttribute{Computed: true, Description: "Parent VPC ID."}
		a["auto_assign_public_ipv4"] = schema.BoolAttribute{Computed: true, Description: "Default public IPv4 allocation for new interfaces."}
		a["auto_assign_public_ipv6"] = schema.BoolAttribute{Computed: true, Description: "Default public IPv6 allocation for new interfaces."}
	case "firewall":
		a["etag"] = schema.StringAttribute{Computed: true, Description: "Strong firewall version."}
		fields := map[string]schema.Attribute{}
		for _, n := range []string{"direction", "protocol", "peer_cidr", "peer_firewall_id", "description"} {
			fields[n] = schema.StringAttribute{Computed: true, Description: "Firewall rule " + n + "."}
		}
		for _, n := range []string{"ports_from", "ports_to", "icmp_type", "icmp_code"} {
			fields[n] = schema.Int64Attribute{Computed: true, Description: "Firewall rule " + n + "."}
		}
		a["rules"] = schema.SetNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: fields}, Description: "Complete firewall rules."}
	}
	resp.Schema = schema.Schema{Description: "Look up an existing EZGH Cloud " + d.kind + " by its canonical ID.", Attributes: a}
}
func (d *lookup) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	d.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", fmt.Sprintf("Expected API client, got %T", req.ProviderData))
	}
}
func (d *lookup) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var id, project types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &project)...)
	if resp.Diagnostics.HasError() {
		return
	}
	prefix := map[string]string{"vpc": "vpc_", "subnet": "sbn_", "firewall": "fwl_"}[d.kind]
	if !validSlug(id.ValueString(), prefix) {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid resource ID", "Use the canonical prefixed resource ID.")
		return
	}
	service, collection := "network", d.kind+"s"
	if d.kind == "firewall" {
		service = "firewall"
	}
	response, err := d.client.Do(ctx, service, "GET", client.ProjectPath(d.client.OrganizationID, project.ValueString(), collection, id.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Look up "+d.kind, err.Error())
		return
	}
	resp.State.Raw = req.Config.Raw
	values := map[string]any{"organization_id": d.client.OrganizationID}
	switch d.kind {
	case "vpc":
		var v vpcWire
		if err = response.Decode(&v); err != nil {
			resp.Diagnostics.AddError("Decode VPC", err.Error())
			return
		}
		var m vpcModel
		m.fromWire(ctx, v)
		values["id"], values["name"], values["description"], values["tags"], values["resource_name"], values["status"] = m.ID, m.Name, m.Description, m.Tags, m.ResourceName, m.Status
		values["ipv4_cidr"], values["ipv6_cidr"], values["default_firewall_id"] = m.IPv4CIDR, m.IPv6CIDR, m.DefaultFirewallID
	case "subnet":
		var v subnetWire
		if err = response.Decode(&v); err != nil {
			resp.Diagnostics.AddError("Decode subnet", err.Error())
			return
		}
		var m subnetModel
		m.fromWire(ctx, v)
		values["id"], values["name"], values["description"], values["tags"], values["resource_name"], values["status"] = m.ID, m.Name, m.Description, m.Tags, m.ResourceName, m.Status
		values["ipv4_cidr"], values["ipv6_cidr"], values["vpc_id"], values["auto_assign_public_ipv4"], values["auto_assign_public_ipv6"] = m.IPv4CIDR, m.IPv6CIDR, m.VPCID, m.AutoAssignPublicIPv4, m.AutoAssignPublicIPv6
	case "firewall":
		var v firewallWire
		if err = response.Decode(&v); err != nil {
			resp.Diagnostics.AddError("Decode firewall", err.Error())
			return
		}
		var m firewallModel
		m.fromWire(ctx, v, response.Header.Get("ETag"))
		values["id"], values["name"], values["description"], values["tags"], values["resource_name"], values["status"], values["rules"], values["etag"] = m.ID, m.Name, m.Description, m.Tags, m.ResourceName, m.Status, m.Rules, m.ETag
	}
	for name, value := range values {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), value)...)
	}
}
