package iam

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func parseImport(id string, n int) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != n {
		return nil, fmt.Errorf("expected %d slash-separated, URL-escaped import components", n)
	}
	for i, p := range parts {
		v, e := url.PathUnescape(p)
		if e != nil || v == "" {
			return nil, fmt.Errorf("invalid import component %d", i+1)
		}
		parts[i] = v
	}
	return parts, nil
}
func toWire(ctx context.Context, v attr.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if v.IsUnknown() {
		return nil, fmt.Errorf("attribute must be known before apply")
	}
	tv, e := v.ToTerraformValue(ctx)
	if e != nil {
		return nil, e
	}
	return terraformWire(tv)
}
func terraformWire(v tftypes.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if !v.IsKnown() {
		return nil, fmt.Errorf("unknown nested value")
	}
	switch typ := v.Type().(type) {
	case tftypes.List, tftypes.Set, tftypes.Tuple:
		var vs []tftypes.Value
		if e := v.As(&vs); e != nil {
			return nil, e
		}
		out := make([]any, 0, len(vs))
		for _, x := range vs {
			w, e := terraformWire(x)
			if e != nil {
				return nil, e
			}
			out = append(out, w)
		}
		return out, nil
	case tftypes.Object, tftypes.Map:
		var vs map[string]tftypes.Value
		if e := v.As(&vs); e != nil {
			return nil, e
		}
		out := map[string]any{}
		for k, x := range vs {
			if x.IsNull() {
				continue
			}
			w, e := terraformWire(x)
			if e != nil {
				return nil, e
			}
			out[k] = w
		}
		return out, nil
	default:
		switch {
		case typ.Equal(tftypes.String):
			var x string
			e := v.As(&x)
			return x, e
		case typ.Equal(tftypes.Bool):
			var x bool
			e := v.As(&x)
			return x, e
		case typ.Equal(tftypes.Number):
			var x big.Float
			if e := v.As(&x); e != nil {
				return nil, e
			}
			i, accuracy := x.Int64()
			if accuracy != big.Exact {
				return nil, fmt.Errorf("number must be an exact int64")
			}
			return i, nil
		}
	}
	return nil, fmt.Errorf("unsupported value type %s", v.Type())
}
func valueFromWire(ctx context.Context, typ attr.Type, v any) (attr.Value, error) {
	raw, e := wireTerraform(typ.TerraformType(ctx), v)
	if e != nil {
		return nil, e
	}
	return typ.ValueFromTerraform(ctx, raw)
}
func wireTerraform(typ tftypes.Type, v any) (tftypes.Value, error) {
	if v == nil {
		return tftypes.NewValue(typ, nil), nil
	}
	switch t := typ.(type) {
	case tftypes.Object:
		m, ok := v.(map[string]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected object")
		}
		out := map[string]tftypes.Value{}
		for k, at := range t.AttributeTypes {
			x, e := wireTerraform(at, m[k])
			if e != nil {
				return tftypes.Value{}, e
			}
			out[k] = x
		}
		return tftypes.NewValue(typ, out), nil
	case tftypes.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected map")
		}
		out := map[string]tftypes.Value{}
		for k, w := range m {
			x, e := wireTerraform(t.ElementType, w)
			if e != nil {
				return tftypes.Value{}, e
			}
			out[k] = x
		}
		return tftypes.NewValue(typ, out), nil
	case tftypes.List:
		return wireSequence(typ, t.ElementType, v)
	case tftypes.Set:
		return wireSequence(typ, t.ElementType, v)
	default:
		if typ.Equal(tftypes.Number) {
			switch n := v.(type) {
			case float64:
				v = big.NewFloat(n)
			case int64:
				v = big.NewFloat(0).SetInt64(n)
			}
		}
		if err := tftypes.ValidateValue(typ, v); err != nil {
			return tftypes.Value{}, err
		}
		return tftypes.NewValue(typ, v), nil
	}
}
func wireSequence(typ, elem tftypes.Type, v any) (tftypes.Value, error) {
	vs, ok := v.([]any)
	if !ok {
		return tftypes.Value{}, fmt.Errorf("expected list")
	}
	out := make([]tftypes.Value, 0, len(vs))
	for _, w := range vs {
		x, e := wireTerraform(elem, w)
		if e != nil {
			return tftypes.Value{}, e
		}
		out = append(out, x)
	}
	return tftypes.NewValue(typ, out), nil
}
func nullValues(d definition) map[string]attr.Value {
	ctx := context.Background()
	out := map[string]attr.Value{}
	for k, f := range d.fields {
		v, _ := f.typ.ValueFromTerraform(ctx, tftypes.NewValue(f.typ.TerraformType(ctx), nil))
		out[k] = v
	}
	return out
}
func mergeResponse(ctx context.Context, d definition, values map[string]attr.Value, body map[string]any) (map[string]attr.Value, error) {
	for k, f := range d.fields {
		if f.wire == "" || f.preserve {
			continue
		}
		if previous, ok := values[k]; ok && previous.IsNull() && f.attribute.IsOptional() && !f.attribute.IsComputed() && !f.output {
			// An omitted optional input stays omitted; the server may return a default.
			continue
		}
		w, ok := body[f.wire]
		if !ok {
			continue
		}
		v, e := valueFromWire(ctx, f.typ, w)
		if e != nil {
			return nil, fmt.Errorf("invalid response field %s: %w", k, e)
		}
		values[k] = v
	}
	return values, nil
}
func stringValue(v map[string]attr.Value, k string) string {
	if x, ok := v[k].(types.String); ok {
		return x.ValueString()
	}
	return ""
}
