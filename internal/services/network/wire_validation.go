package network

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
)

func validSlug(slug, prefix string) bool {
	if !strings.HasPrefix(slug, prefix) || len(slug) != len(prefix)+12 {
		return false
	}
	for _, c := range slug[len(prefix):] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func validAddressWire(slug, prefix, name, ipv4, ipv6, status, resourceName string) bool {
	if !validSlug(slug, prefix) || name == "" || resourceName == "" || (status != "available" && status != "deleting") {
		return false
	}
	v4, err4 := netip.ParsePrefix(ipv4)
	v6, err6 := netip.ParsePrefix(ipv6)
	return err4 == nil && err6 == nil && v4.Addr().Is4() && v6.Addr().Is6()
}
func (v *vpcWire) UnmarshalJSON(b []byte) error {
	type wire vpcWire
	var decoded wire
	if err := json.Unmarshal(b, &decoded); err != nil {
		return errors.New("invalid VPC JSON response")
	}
	if !validAddressWire(decoded.Slug, "vpc_", decoded.Name, decoded.IPv4CIDR, decoded.IPv6CIDR, decoded.Status, decoded.ResourceName) {
		return errors.New("VPC response is missing valid identity or required fields")
	}
	*v = vpcWire(decoded)
	return nil
}
func (v *subnetWire) UnmarshalJSON(b []byte) error {
	type wire subnetWire
	var decoded wire
	if err := json.Unmarshal(b, &decoded); err != nil {
		return errors.New("invalid subnet JSON response")
	}
	if !validAddressWire(decoded.Slug, "sbn_", decoded.Name, decoded.IPv4CIDR, decoded.IPv6CIDR, decoded.Status, decoded.ResourceName) || !validSlug(decoded.VPCID, "vpc_") {
		return errors.New("subnet response is missing valid identity or required fields")
	}
	*v = subnetWire(decoded)
	return nil
}
func (v *firewallWire) UnmarshalJSON(b []byte) error {
	type wire firewallWire
	var decoded wire
	if err := json.Unmarshal(b, &decoded); err != nil {
		return errors.New("invalid firewall JSON response")
	}
	if !validSlug(decoded.Slug, "fwl_") || decoded.Name == "" || decoded.Status != "active" || decoded.ResourceName == "" || decoded.Rules == nil {
		return errors.New("firewall response is missing valid identity or required fields")
	}
	if _, err := encodeRules(decodeRules(decoded.Rules)); err != nil {
		return errors.New("firewall response contains invalid rules")
	}
	*v = firewallWire(decoded)
	return nil
}
