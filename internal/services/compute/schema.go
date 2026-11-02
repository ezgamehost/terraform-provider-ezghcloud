package compute

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type planmodifierString = planmodifier.String

func computedID() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: "Canonical prefixed resource identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}
func organizationAttribute() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: "Organization managed by the configured provider.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}
func projectAttribute() schema.StringAttribute {
	return schema.StringAttribute{Required: true, Description: "Project identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
}
func tagsAttribute() schema.MapAttribute {
	return schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})), Description: "Resource tags."}
}
