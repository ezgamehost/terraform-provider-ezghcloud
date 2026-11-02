package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ezgamehost/terraform-provider-ezghcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestImportsAreScopedAndCanonical(t *testing.T) {
	org, project, id, err := parseImport("org_test/prj_test/vpc_abcdefghijkl", "vpc_")
	if err != nil || org != "org_test" || project != "prj_test" || id != "vpc_abcdefghijkl" {
		t.Fatalf("%q %q %q %v", org, project, id, err)
	}
	for _, id := range []string{"vpc_abcdefghijkl", "org_test//vpc_abcdefghijkl", "org_test/prj_test/fwl_abcdefghijkl"} {
		if _, _, _, err := parseImport(id, "vpc_"); err == nil {
			t.Errorf("accepted invalid import %q", id)
		}
	}
}

func initialVPC() vpcModel {
	return vpcModel{
		ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("private"), Description: types.StringNull(), Tags: types.MapValueMust(types.StringType, map[string]attr.Value{}), ResourceName: types.StringUnknown(), Status: types.StringUnknown(), IPv4CIDR: types.StringValue("10.0.0.0/16"), IPv6CIDR: types.StringUnknown(), DefaultFirewallID: types.StringUnknown(),
	}
}
func TestVPCLifecycleAndMissingParentPreservesState(t *testing.T) {
	ctx := context.Background()
	deleted := false
	denyParent := false
	wire := vpcWire{Slug: "vpc_abcdefghijkl", Name: "private", IPv4CIDR: "10.0.0.0/16", IPv6CIDR: "fd00::/56", Status: "available", ResourceName: "ezgh:test", Tags: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/vpcs") {
			switch r.Method {
			case "POST":
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["ipv4Cidr"] != "10.0.0.0/16" {
					t.Error(body)
				}
				w.WriteHeader(201)
			case "PATCH":
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if _, ok := body["ipv4Cidr"]; ok {
					t.Error("immutable field patched")
				}
				wire.Name = body["name"].(string)
			case "GET":
				if deleted {
					w.WriteHeader(404)
					return
				}
			case "DELETE":
				deleted = true
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(wire)
			return
		}
		if denyParent {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"id":"prj_test"}`))
	}))
	defer server.Close()
	r := &vpcResource{client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"network": server.URL, "orgs": server.URL}})}
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	plan := tfsdk.Plan{Schema: s.Schema}
	if diags := plan.Set(ctx, initialVPC()); diags.HasError() {
		t.Fatal(diags)
	}
	created := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	var m vpcModel
	if diags := created.State.Get(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}
	if m.ID.ValueString() != wire.Slug {
		t.Fatal(m.ID)
	}
	m.Name = types.StringValue("renamed")
	if diags := plan.Set(ctx, m); diags.HasError() {
		t.Fatal(diags)
	}
	updated := resource.UpdateResponse{State: created.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	deleted = true
	denyParent = true
	read := resource.ReadResponse{State: updated.State}
	r.Read(ctx, resource.ReadRequest{State: updated.State}, &read)
	if !read.Diagnostics.HasError() || read.State.Raw.IsNull() {
		t.Fatal("parent denial discarded resource state")
	}
	denyParent = false
	read = resource.ReadResponse{State: updated.State}
	r.Read(ctx, resource.ReadRequest{State: updated.State}, &read)
	if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
		t.Fatalf("confirmed absence: %v", read.Diagnostics)
	}
}

func TestMalformedVPCResponseCannotEraseIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer server.Close()
	r := &vpcResource{client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"network": server.URL}})}
	ctx := context.Background()
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	m := initialVPC()
	m.ID = types.StringValue("vpc_abcdefghijkl")
	m.OrganizationID = types.StringValue("org_test")
	state := tfsdk.State{Schema: s.Schema}
	if diags := state.Set(ctx, m); diags.HasError() {
		t.Fatal(diags)
	}
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	if !read.Diagnostics.HasError() {
		t.Fatal("malformed API response accepted")
	}
	var got vpcModel
	read.State.Get(ctx, &got)
	if got.ID.ValueString() != m.ID.ValueString() {
		t.Fatal("identity overwritten")
	}
}

func TestFirewallRulesPreserveNullAndEmpty(t *testing.T) {
	rules := []ruleModel{{Direction: types.StringValue("inbound"), Protocol: types.StringValue("tcp"), PeerCIDR: types.StringValue("10.0.0.0/8"), PeerFirewallID: types.StringNull(), PortsFrom: types.Int64Value(22), PortsTo: types.Int64Value(22), ICMPType: types.Int64Null(), ICMPCode: types.Int64Null(), Description: types.StringValue("")}}
	wire, err := encodeRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire[0].Ports == nil || wire[0].Ports.From != 22 || wire[0].ICMP != nil || wire[0].Peer.CIDR != "10.0.0.0/8" {
		t.Fatalf("%+v", wire)
	}
	decoded := decodeRules(wire)
	if !decoded[0].ICMPCode.IsNull() || !decoded[0].PeerFirewallID.IsNull() || decoded[0].PortsFrom.ValueInt64() != 22 {
		t.Fatalf("%+v", decoded)
	}
	empty, err := encodeRules(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty rules must be [], got %+v %v", empty, err)
	}
}

func TestFirewallRejectsAmbiguousPeersAndInvalidPorts(t *testing.T) {
	base := ruleModel{Direction: types.StringValue("inbound"), Protocol: types.StringValue("tcp"), PeerCIDR: types.StringValue("10.0.0.0/8"), PeerFirewallID: types.StringNull(), PortsFrom: types.Int64Null(), PortsTo: types.Int64Null(), ICMPType: types.Int64Null(), ICMPCode: types.Int64Null()}
	invalid := base
	invalid.PeerFirewallID = types.StringValue("fwl_abcdefghijkl")
	if _, err := encodeRules([]ruleModel{invalid}); err == nil {
		t.Fatal("accepted two peers")
	}
	invalid = base
	invalid.PortsFrom = types.Int64Value(20)
	invalid.PortsTo = types.Int64Value(10)
	if _, err := encodeRules([]ruleModel{invalid}); err == nil {
		t.Fatal("accepted inverted port range")
	}
}

func TestResourceSchemasValidate(t *testing.T) {
	for _, newResource := range Resources() {
		r := newResource()
		var response resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &response)
		if diags := response.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Fatal(diags)
		}
	}
}

func initialFirewall() firewallModel {
	return firewallModel{
		ID: types.StringUnknown(), OrganizationID: types.StringUnknown(), ProjectID: types.StringValue("prj_test"), Name: types.StringValue("private"), Description: types.StringValue(""), Tags: types.MapValueMust(types.StringType, map[string]attr.Value{}), ResourceName: types.StringUnknown(), Status: types.StringUnknown(), ETag: types.StringUnknown(), Rules: types.SetValueMust(types.ObjectType{AttrTypes: ruleTypes()}, nil),
	}
}

func TestFirewallOwnsEmptyRulesAndUsesConditionalReplacement(t *testing.T) {
	ctx := context.Background()
	etag := `"one"`
	replaced := false
	wire := firewallWire{Slug: "fwl_abcdefghijkl", Name: "private", Status: "active", ResourceName: "ezgh:test", Tags: map[string]string{}, Rules: []ruleWire{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		switch r.Method {
		case "POST":
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if string(body["rules"]) != "[]" {
				t.Fatalf("create rules not explicit empty: %s", body["rules"])
			}
			w.WriteHeader(201)
		case "PUT":
			if r.Header.Get("If-Match") != etag {
				t.Error("missing conditional version")
			}
			var body struct {
				Rules []ruleWire `json:"rules"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			wire.Rules = body.Rules
			replaced = true
			etag = `"two"`
			w.Header().Set("ETag", etag)
			json.NewEncoder(w).Encode(map[string]any{"firewall": wire, "ruleIds": []string{}})
			return
		}
		json.NewEncoder(w).Encode(wire)
	}))
	defer server.Close()
	r := &firewallResource{client: client.New(client.Config{OrganizationID: "org_test", Endpoints: map[string]string{"firewall": server.URL}})}
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	plan := tfsdk.Plan{Schema: s.Schema}
	if diags := plan.Set(ctx, initialFirewall()); diags.HasError() {
		t.Fatal(diags)
	}
	created := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	var m firewallModel
	created.State.Get(ctx, &m)
	rules := []ruleModel{{Direction: types.StringValue("outbound"), Protocol: types.StringValue("all"), PeerCIDR: types.StringValue("0.0.0.0/0"), PeerFirewallID: types.StringNull(), PortsFrom: types.Int64Null(), PortsTo: types.Int64Null(), ICMPType: types.Int64Null(), ICMPCode: types.Int64Null(), Description: types.StringValue("")}}
	m.Rules, _ = types.SetValueFrom(ctx, types.ObjectType{AttrTypes: ruleTypes()}, rules)
	if diags := plan.Set(ctx, m); diags.HasError() {
		t.Fatal(diags)
	}
	updated := resource.UpdateResponse{State: created.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	if !replaced {
		t.Fatal("rules weren't replaced")
	}
	var got firewallModel
	updated.State.Get(ctx, &got)
	if !got.Rules.Equal(m.Rules) {
		t.Fatalf("rules changed semantic representation: %s", got.Rules)
	}
	// A changed server version after planning must not overwrite concurrent rules.
	etag = `"three"`
	replaced = false
	refused := resource.UpdateResponse{State: updated.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: updated.State}, &refused)
	if !refused.Diagnostics.HasError() || replaced {
		t.Fatal("concurrent rules were overwritten")
	}
}

func TestRuleDatabasePeer(t *testing.T) {
	rule := ruleModel{Direction: types.StringValue("inbound"), Protocol: types.StringValue("tcp"), PeerDatabaseID: types.StringValue("dbi_4k2m9x0a7qpe"), PortsFrom: types.Int64Value(5432), PortsTo: types.Int64Value(5432)}
	wire, err := encodeRules([]ruleModel{rule})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wire[0].Peer)
	if string(raw) != `{"database":"dbi_4k2m9x0a7qpe"}` {
		t.Fatalf("peer = %s", raw)
	}
	decoded := decodeRules(wire)
	if decoded[0].PeerDatabaseID.ValueString() != "dbi_4k2m9x0a7qpe" || !decoded[0].PeerCIDR.IsNull() || !decoded[0].PeerFirewallID.IsNull() {
		t.Fatalf("%+v", decoded[0])
	}
	rule.PeerFirewallID = types.StringValue("fwl_abcdefghijkl")
	if _, err := encodeRules([]ruleModel{rule}); err == nil {
		t.Fatal("accepted a database and a firewall peer together")
	}
}
