package compute

import (
	"context"
	"fmt"
	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func importInterfaces(ctx context.Context, c *client.Client, project string, ids []string) (types.List, error) {
	values := []attr.Value{}
	for _, id := range ids {
		out, err := c.Do(ctx, "network", "GET", client.ProjectPath(c.OrganizationID, project, "network-interfaces", id), nil)
		if err != nil {
			return types.ListNull(types.ObjectType{AttrTypes: interfaceTypes}), err
		}
		var v struct {
			Slug       string
			Subnet     *string
			Attachment *struct{ DeleteOnTermination bool }
		}
		if err = out.Decode(&v); err != nil {
			return types.ListNull(types.ObjectType{AttrTypes: interfaceTypes}), err
		}
		if v.Slug == "" || v.Attachment == nil {
			return types.ListNull(types.ObjectType{AttrTypes: interfaceTypes}), fmt.Errorf("cannot import interface without its identity and attachment")
		}
		fields := map[string]attr.Value{"subnet_id": types.StringNull(), "network_interface_id": types.StringNull(), "private_ipv4_address": types.StringNull(), "ipv4": types.BoolNull(), "ipv6": types.BoolNull(), "firewall_ids": types.ListNull(types.StringType)}
		if !v.Attachment.DeleteOnTermination {
			fields["network_interface_id"] = types.StringValue(v.Slug)
		} else {
			fields["subnet_id"] = nullableString(v.Subnet)
		}
		obj, d := types.ObjectValue(interfaceTypes, fields)
		if d.HasError() {
			return types.ListNull(types.ObjectType{AttrTypes: interfaceTypes}), fmt.Errorf("cannot encode imported interface: %v", d)
		}
		values = append(values, obj)
	}
	result, d := types.ListValue(types.ObjectType{AttrTypes: interfaceTypes}, values)
	if d.HasError() {
		return result, fmt.Errorf("cannot encode imported interfaces: %v", d)
	}
	return result, nil
}
