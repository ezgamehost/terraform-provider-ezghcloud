package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func bodyFor(ctx context.Context, d definition, v map[string]attr.Value, updating bool) (map[string]any, error) {
	body := map[string]any{}
	for k, f := range d.fields {
		if f.output || f.wire == "" || f.preserve || (updating && f.replace) {
			continue
		}
		x := v[k]
		if x == nil || x.IsNull() {
			continue
		}
		if x.IsUnknown() && f.attribute.IsOptional() && f.attribute.IsComputed() {
			continue
		}
		w, e := toWire(ctx, x)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", k, e)
		}
		body[f.wire] = w
	}
	if d.name == "oidc_provider" {
		if raw := stringValue(v, "jwks_json"); raw != "" {
			var jwks map[string]any
			if err := json.Unmarshal([]byte(raw), &jwks); err != nil {
				return nil, errors.New("jwks_json must be a JSON object containing public keys")
			}
			body["jwks"] = jwks
		}
	}
	if d.name == "policy" {
		if x, ok := body["statements"]; ok {
			body["document"] = map[string]any{"statements": x}
			delete(body, "statements")
		}
	}
	if d.name == "bot_trust" {
		if xs, ok := body["trust"].([]any); ok {
			for _, x := range xs {
				m := x.(map[string]any)
				m["principalType"] = m["principal_type"]
				m["resourceName"] = m["resource_name"]
				delete(m, "principal_type")
				delete(m, "resource_name")
			}
		}
	}
	return body, nil
}
func entityPath(c *client.Client, d definition, v map[string]attr.Value, collection bool) string {
	id := stringValue(v, "id")
	switch d.name {
	case "bot_trust":
		return client.OrgPath(c.OrganizationID, "bots", stringValue(v, "bot_id"), "trust")
	case "bot_oidc_trust":
		p := client.OrgPath(c.OrganizationID, "bots", stringValue(v, "bot_id"), "oidc-trust")
		if !collection {
			p += "/" + url.PathEscape(id)
		}
		return p
	default:
		if collection {
			return client.OrgPath(c.OrganizationID, d.collection)
		}
		return client.OrgPath(c.OrganizationID, d.collection, id)
	}
}
func decodeBody(r *client.Response) (map[string]any, error) {
	var body map[string]any
	if e := r.Decode(&body); e != nil {
		return nil, e
	}
	return body, nil
}
func flattenBody(d definition, body map[string]any) map[string]any {
	if d.name == "policy" {
		if document, ok := body["document"].(map[string]any); ok {
			body["statements"] = document["statements"]
		}
	}
	if d.name == "bot_oidc_trust" {
		if p, ok := body["provider"].(map[string]any); ok {
			body["providerId"] = p["id"]
		}
	}
	if d.name == "bot_trust" {
		if xs, ok := body["trust"].([]any); ok {
			for _, x := range xs {
				if m, ok := x.(map[string]any); ok {
					m["principal_type"] = m["principalType"]
					m["resource_name"] = m["resourceName"]
				}
			}
		}
	}
	return body
}
func createEntity(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value) (map[string]attr.Value, error) {
	body, e := bodyFor(ctx, d, v, false)
	if e != nil {
		return v, e
	}
	method := http.MethodPost
	var headers http.Header
	if d.name == "bot_trust" {
		method = http.MethodPut
		headers, e = etag(ctx, c, entityPath(c, d, v, true))
		if e != nil {
			return v, e
		}
	}
	r, e := c.DoWithHeaders(ctx, "orgs", method, entityPath(c, d, v, true), body, headers)
	if e != nil {
		return v, e
	}
	out, e := decodeBody(r)
	if e != nil {
		return v, e
	}
	v["organization_id"] = types.StringValue(c.OrganizationID)
	if d.name == "bot_trust" {
		out["id"] = stringValue(v, "bot_id")
	}
	if d.name == "api_key" {
		token, _ := out["token"].(string)
		if key, ok := out["apiKey"].(map[string]any); ok {
			out = key
		}
		if id, ok := out["id"].(string); !ok || id == "" {
			return v, errors.New("API key response omitted its identity")
		}
		v["token"] = types.StringValue(token)
		v, e = mergeResponse(ctx, d, v, out)
		if e != nil {
			return v, e
		}
		if token == "" {
			return v, errors.New("API key was created but its one-time token was unavailable; its identity was retained; revoke or import it before explicitly creating a replacement")
		}
		return v, nil
	}
	if id, ok := out["id"].(string); !ok || id == "" {
		return v, errors.New("IAM create response omitted resource identity")
	}
	return mergeResponse(ctx, d, v, flattenBody(d, out))
}
func etag(ctx context.Context, c *client.Client, p string) (http.Header, error) {
	r, e := c.Do(ctx, "orgs", http.MethodGet, p, nil)
	if e != nil {
		return nil, e
	}
	tag := r.Header.Get("ETag")
	if tag == "" {
		return nil, errors.New("API response omitted ETag; refusing an unconditional IAM write")
	}
	return http.Header{"If-Match": []string{tag}}, nil
}
func readEntity(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value) (map[string]attr.Value, bool, error) {
	if d.name == "api_key" || d.name == "bot_oidc_trust" {
		if d.name == "api_key" {
			r, e := c.Do(ctx, "orgs", http.MethodGet, client.OrgPath(c.OrganizationID, "permissions")+"?actions=apiKeys.list", nil)
			if e != nil {
				return v, false, e
			}
			b, e := decodeBody(r)
			if e != nil {
				return v, false, e
			}
			allowed, ok := b["allowed"].([]any)
			if !ok || len(allowed) == 0 {
				return v, false, errors.New("apiKeys.list is required to authoritatively refresh API key state")
			}
		}
		rows, e := c.List(ctx, "orgs", entityPath(c, d, v, true))
		if e != nil {
			return v, false, e
		}
		for _, row := range rows {
			if id, ok := row["id"].(string); !ok || id == "" {
				return v, false, errors.New("IAM collection contained an item without its identity")
			}
			if row["id"] == stringValue(v, "id") {
				out, e := mergeResponse(ctx, d, v, flattenBody(d, row))
				return out, false, e
			}
		}
		return v, true, nil
	}
	r, e := c.Do(ctx, "orgs", http.MethodGet, entityPath(c, d, v, false), nil)
	if e != nil {
		var apiErr *client.Error
		if errors.As(e, &apiErr) && apiErr.StatusCode == 404 {
			_, parentErr := c.Do(ctx, "orgs", http.MethodGet, "/v1/organizations/"+url.PathEscape(c.OrganizationID), nil)
			if parentErr != nil {
				return v, false, parentErr
			}
			return v, true, nil
		}
		return v, false, e
	}
	body, e := decodeBody(r)
	if e != nil {
		return v, false, e
	}
	if d.name == "policy" && d.kind != "lookup" && (body["type"] == "managed" || body["managedBy"] != nil) {
		return v, false, errors.New("managed policies are read-only; use an ezghcloud_policy data source")
	}
	if d.name == "bot_trust" {
		if _, ok := body["trust"].([]any); !ok {
			return v, false, errors.New("API response omitted bot trust collection")
		}
		body["id"] = stringValue(v, "bot_id")
	}
	if id, ok := body["id"].(string); !ok || id == "" {
		return v, false, errors.New("IAM response omitted resource identity")
	}
	v, e = mergeResponse(ctx, d, v, flattenBody(d, body))
	return v, false, e
}
func updateEntity(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value) (map[string]attr.Value, error) {
	p := entityPath(c, d, v, false)
	headers, e := etag(ctx, c, p)
	if e != nil {
		return v, e
	}
	body, e := bodyFor(ctx, d, v, true)
	if e != nil {
		return v, e
	}
	method := http.MethodPatch
	if d.name == "bot_trust" {
		method = http.MethodPut
	}
	r, e := c.DoWithHeaders(ctx, "orgs", method, p, body, headers)
	if e != nil {
		return v, e
	}
	out, e := decodeBody(r)
	if e != nil {
		return v, e
	}
	return mergeResponse(ctx, d, v, flattenBody(d, out))
}
func deleteEntity(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value) error {
	p := entityPath(c, d, v, false)
	var headers http.Header
	var e error
	if d.name == "invitation" {
		r, err := c.Do(ctx, "orgs", http.MethodGet, p, nil)
		if err != nil {
			return err
		}
		b, err := decodeBody(r)
		if err != nil {
			return err
		}
		status, _ := b["status"].(string)
		if status != "pending" {
			return nil
		}
	}
	if d.name == "oidc_provider" || d.name == "bot_trust" {
		headers, e = etag(ctx, c, p)
		if e != nil {
			return e
		}
	}
	method := http.MethodDelete
	var body any
	if d.name == "bot_trust" {
		method = http.MethodPut
		body = map[string]any{"trust": []any{}}
	}
	_, e = c.DoWithHeaders(ctx, "orgs", method, p, body, headers)
	return e
}
func principalCollection(kind string) (string, error) {
	switch kind {
	case "user":
		return "users", nil
	case "bot":
		return "bots", nil
	case "group":
		return "groups", nil
	case "ssoUser":
		return "sso-users", nil
	case "ssoGroup":
		return "sso-groups", nil
	}
	return "", errors.New("principal_type must be user, bot, group, ssoUser or ssoGroup")
}
func relationPaths(c *client.Client, d definition, v map[string]attr.Value) (parent, p string, e error) {
	if d.name == "group_membership" {
		kind := stringValue(v, "principal_type")
		if kind != "user" && kind != "bot" {
			return "", "", errors.New("group memberships require principal_type user or bot")
		}
		parent = client.OrgPath(c.OrganizationID, "groups", stringValue(v, "group_id"))
		return parent, parent + "/members/" + url.PathEscape(stringValue(v, "principal_id")), nil
	}
	collection, e := principalCollection(stringValue(v, "principal_type"))
	if e != nil {
		return "", "", e
	}
	parent = client.OrgPath(c.OrganizationID, collection, stringValue(v, "principal_id"))
	p = parent + "/policies/" + url.PathEscape(stringValue(v, "policy_id"))
	if strings.HasPrefix(collection, "sso-") {
		parent += "/policies"
	}
	return parent, p, nil
}
func changeRelation(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value, method string) (map[string]attr.Value, error) {
	parent, p, e := relationPaths(c, d, v)
	if e != nil {
		return v, e
	}
	var headers http.Header
	if !strings.HasPrefix(stringValue(v, "principal_type"), "sso") {
		headers, e = etag(ctx, c, parent)
		if e != nil {
			return v, e
		}
	}
	_, e = c.DoWithHeaders(ctx, "orgs", method, p, nil, headers)
	if e != nil {
		return v, e
	}
	v["organization_id"] = types.StringValue(c.OrganizationID)
	parts := []string{stringValue(v, "principal_type"), stringValue(v, "principal_id")}
	if d.name == "group_membership" {
		parts = append(parts, stringValue(v, "group_id"))
	} else {
		parts = append(parts, stringValue(v, "policy_id"))
	}
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	v["id"] = types.StringValue(strings.Join(parts, "/"))
	return v, nil
}
func readRelation(ctx context.Context, c *client.Client, d definition, v map[string]attr.Value) (bool, error) {
	parent, _, e := relationPaths(c, d, v)
	if e != nil {
		return false, e
	}
	r, e := c.Do(ctx, "orgs", http.MethodGet, parent, nil)
	if e != nil {
		return false, e
	}
	body, e := decodeBody(r)
	if e != nil {
		return false, e
	}
	if d.name == "group_membership" {
		xs, ok := body["members"].([]any)
		if !ok {
			return false, errors.New("API omitted group members")
		}
		for _, x := range xs {
			m, ok := x.(map[string]any)
			if ok && m["id"] == stringValue(v, "principal_id") && m["type"] == stringValue(v, "principal_type") {
				return true, nil
			}
		}
		return false, nil
	}
	xs, ok := body["policyIds"].([]any)
	if !ok {
		return false, errors.New("API omitted policyIds")
	}
	for _, x := range xs {
		if x == stringValue(v, "policy_id") {
			return true, nil
		}
	}
	return false, nil
}
