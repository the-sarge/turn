// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import (
	"net"
	"net/netip"
)

// canonicalMode selects how canonicalAddrPort treats IPv4-mapped IPv6 input.
// Every other rule is shared: the address must be valid, zone-free, unicast
// (neither unspecified nor multicast), and carry a nonzero port.
type canonicalMode uint8

const (
	// canonicalStrict rejects IPv4-mapped IPv6 input. It governs values the
	// caller spells explicitly, such as ClientConfig.Server: the caller must
	// use the IPv4 literal.
	canonicalStrict canonicalMode = iota

	// canonicalUnmap unmaps IPv4-mapped IPv6 input. It governs values the
	// network reports, such as inbound datagram sources, which a dual-stack
	// socket presents in mapped form.
	canonicalUnmap
)

// canonicalAddrPort is the single owner of the canonical netip.AddrPort form
// used at the public boundary: unmapped, zone-free, nonzero port, neither
// unspecified nor multicast. It reports false, with the zero value, for input
// that cannot be canonicalized under mode.
func canonicalAddrPort(ap netip.AddrPort, mode canonicalMode) (netip.AddrPort, bool) {
	if !ap.IsValid() {
		return netip.AddrPort{}, false
	}
	addr := ap.Addr()
	if addr.Is4In6() {
		if mode == canonicalStrict {
			return netip.AddrPort{}, false
		}
		addr = addr.Unmap()
	}
	if ap.Port() == 0 || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() {
		return netip.AddrPort{}, false
	}

	return netip.AddrPortFrom(addr, ap.Port()), true
}

// canonicalSourceAddr converts an inbound datagram source to its canonical
// form. Only a non-nil *net.UDPAddr is a candidate; every other dynamic type
// reports false. The conversion guards the two lossy steps of
// (*net.UDPAddr).AddrPort — zone dropping on IPv4 and port truncation to
// uint16 — before handing the value to canonicalAddrPort in unmap mode.
func canonicalSourceAddr(from net.Addr) (netip.AddrPort, bool) {
	udpAddr, ok := from.(*net.UDPAddr)
	if !ok || udpAddr == nil {
		return netip.AddrPort{}, false
	}
	if udpAddr.Zone != "" || udpAddr.Port < 1 || udpAddr.Port > 65535 {
		return netip.AddrPort{}, false
	}

	return canonicalAddrPort(udpAddr.AddrPort(), canonicalUnmap)
}
