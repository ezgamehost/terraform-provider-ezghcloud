package network

import (
	"fmt"
	"net/netip"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type ruleModel struct {
	Direction      types.String `tfsdk:"direction"`
	Protocol       types.String `tfsdk:"protocol"`
	PeerCIDR       types.String `tfsdk:"peer_cidr"`
	PeerFirewallID types.String `tfsdk:"peer_firewall_id"`
	PortsFrom      types.Int64  `tfsdk:"ports_from"`
	PortsTo        types.Int64  `tfsdk:"ports_to"`
	ICMPType       types.Int64  `tfsdk:"icmp_type"`
	ICMPCode       types.Int64  `tfsdk:"icmp_code"`
	Description    types.String `tfsdk:"description"`
}
type ruleWire struct {
	Direction   string     `json:"direction"`
	Protocol    string     `json:"protocol"`
	Peer        rulePeer   `json:"peer"`
	Ports       *rulePorts `json:"ports"`
	ICMP        *ruleICMP  `json:"icmp"`
	Description string     `json:"description"`
}
type rulePeer struct {
	CIDR       string `json:"cidr,omitempty"`
	FirewallID string `json:"firewallId,omitempty"`
}
type rulePorts struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}
type ruleICMP struct {
	Type int64  `json:"type"`
	Code *int64 `json:"code"`
}

func ruleTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"direction": types.StringType, "protocol": types.StringType, "peer_cidr": types.StringType, "peer_firewall_id": types.StringType, "ports_from": types.Int64Type, "ports_to": types.Int64Type, "icmp_type": types.Int64Type, "icmp_code": types.Int64Type, "description": types.StringType,
	}
}
func ruleAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"direction":        schema.StringAttribute{Required: true, Description: "inbound or outbound."},
		"protocol":         schema.StringAttribute{Required: true, Description: "tcp, udp, icmp, icmpv6, all, or another IANA protocol number as a string. Use named forms for 6, 17, 1 and 58."},
		"peer_cidr":        schema.StringAttribute{Optional: true, Description: "Canonical IPv4 or IPv6 peer prefix. Exactly one peer selector is required."},
		"peer_firewall_id": schema.StringAttribute{Optional: true, Description: "Peer firewall ID. Exactly one peer selector is required."},
		"ports_from":       schema.Int64Attribute{Optional: true, Description: "First TCP/UDP port (1–65535); omit both port fields for all ports."},
		"ports_to":         schema.Int64Attribute{Optional: true, Description: "Last TCP/UDP port (1–65535)."},
		"icmp_type":        schema.Int64Attribute{Optional: true, Description: "ICMP type (0–255); omit for every type."},
		"icmp_code":        schema.Int64Attribute{Optional: true, Description: "ICMP code (0–255); omit for every code of the selected type."},
		"description":      schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Rule description."},
	}
}

func encodeRules(models []ruleModel) ([]ruleWire, error) {
	result := make([]ruleWire, 0, len(models))
	for i, m := range models {
		// Unknown fields are checked when they become known at apply. Config validation
		// avoids treating references to resources not created yet as empty strings.
		if m.Direction.IsUnknown() || m.Protocol.IsUnknown() || m.PeerCIDR.IsUnknown() || m.PeerFirewallID.IsUnknown() || m.PortsFrom.IsUnknown() || m.PortsTo.IsUnknown() || m.ICMPType.IsUnknown() || m.ICMPCode.IsUnknown() {
			continue
		}
		fail := func(message string) ([]ruleWire, error) { return nil, fmt.Errorf("rules[%d]: %s", i, message) }
		if m.Direction.ValueString() != "inbound" && m.Direction.ValueString() != "outbound" {
			return fail("direction must be inbound or outbound")
		}
		protocol := m.Protocol.ValueString()
		switch protocol {
		case "tcp", "udp", "icmp", "icmpv6", "all":
		default:
			n, err := strconv.Atoi(protocol)
			if err != nil || n < 0 || n > 255 || strconv.Itoa(n) != protocol {
				return fail("protocol must be a name or canonical IANA protocol number")
			}
			if n == 6 || n == 17 || n == 1 || n == 58 {
				return fail("use the named protocol tcp, udp, icmp or icmpv6")
			}
		}
		cidr, firewall := m.PeerCIDR.ValueString(), m.PeerFirewallID.ValueString()
		if (cidr == "") == (firewall == "") {
			return fail("set exactly one of peer_cidr and peer_firewall_id")
		}
		if cidr != "" {
			p, err := netip.ParsePrefix(cidr)
			if err != nil || p.Masked().String() != cidr {
				return fail("peer_cidr must be a canonical prefix")
			}
		}
		r := ruleWire{Direction: m.Direction.ValueString(), Protocol: protocol, Peer: rulePeer{CIDR: cidr, FirewallID: firewall}, Description: m.Description.ValueString()}
		if m.PortsFrom.IsNull() != m.PortsTo.IsNull() {
			return fail("ports_from and ports_to must be set together")
		}
		if !m.PortsFrom.IsNull() {
			from, to := m.PortsFrom.ValueInt64(), m.PortsTo.ValueInt64()
			if from < 1 || to > 65535 || from > to {
				return fail("ports must form an increasing range from 1 to 65535")
			}
			if protocol != "tcp" && protocol != "udp" {
				return fail("ports require tcp or udp")
			}
			r.Ports = &rulePorts{From: from, To: to}
		}
		if !m.ICMPCode.IsNull() && m.ICMPType.IsNull() {
			return fail("icmp_code requires icmp_type")
		}
		if !m.ICMPType.IsNull() {
			if protocol != "icmp" && protocol != "icmpv6" {
				return fail("icmp_type requires icmp or icmpv6")
			}
			if m.ICMPType.ValueInt64() < 0 || m.ICMPType.ValueInt64() > 255 {
				return fail("icmp_type must be 0–255")
			}
			var code *int64
			if !m.ICMPCode.IsNull() {
				v := m.ICMPCode.ValueInt64()
				if v < 0 || v > 255 {
					return fail("icmp_code must be 0–255")
				}
				code = &v
			}
			r.ICMP = &ruleICMP{Type: m.ICMPType.ValueInt64(), Code: code}
		}
		result = append(result, r)
	}
	return result, nil
}

func decodeRules(rules []ruleWire) []ruleModel {
	result := make([]ruleModel, 0, len(rules))
	for _, r := range rules {
		m := ruleModel{Direction: types.StringValue(r.Direction), Protocol: types.StringValue(r.Protocol), PeerCIDR: types.StringNull(), PeerFirewallID: types.StringNull(), PortsFrom: types.Int64Null(), PortsTo: types.Int64Null(), ICMPType: types.Int64Null(), ICMPCode: types.Int64Null(), Description: types.StringValue(r.Description)}
		if r.Peer.CIDR != "" {
			m.PeerCIDR = types.StringValue(r.Peer.CIDR)
		}
		if r.Peer.FirewallID != "" {
			m.PeerFirewallID = types.StringValue(r.Peer.FirewallID)
		}
		if r.Ports != nil {
			m.PortsFrom = types.Int64Value(r.Ports.From)
			m.PortsTo = types.Int64Value(r.Ports.To)
		}
		if r.ICMP != nil {
			m.ICMPType = types.Int64Value(r.ICMP.Type)
			m.ICMPCode = types.Int64PointerValue(r.ICMP.Code)
		}
		result = append(result, m)
	}
	return result
}
