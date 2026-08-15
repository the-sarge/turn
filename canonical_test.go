// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package turn

import (
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalAddrPort(t *testing.T) {
	mapped := netip.AddrPortFrom(netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: 192, 13: 0, 14: 2, 15: 1}), 3478)
	assert.True(t, mapped.Addr().Is4In6(), "fixture must be IPv4-mapped")

	tests := []struct {
		name       string
		in         netip.AddrPort
		strictOK   bool
		unmapOK    bool
		wantStrict netip.AddrPort
		wantUnmap  netip.AddrPort
	}{
		{
			name:       "IPv4",
			in:         netip.MustParseAddrPort("192.0.2.1:3478"),
			strictOK:   true,
			unmapOK:    true,
			wantStrict: netip.MustParseAddrPort("192.0.2.1:3478"),
			wantUnmap:  netip.MustParseAddrPort("192.0.2.1:3478"),
		},
		{
			name:       "IPv6",
			in:         netip.MustParseAddrPort("[2001:db8::1]:3478"),
			strictOK:   true,
			unmapOK:    true,
			wantStrict: netip.MustParseAddrPort("[2001:db8::1]:3478"),
			wantUnmap:  netip.MustParseAddrPort("[2001:db8::1]:3478"),
		},
		{
			name:      "IPv4-mapped IPv6: strict rejects, unmap canonicalizes",
			in:        mapped,
			strictOK:  false,
			unmapOK:   true,
			wantUnmap: netip.MustParseAddrPort("192.0.2.1:3478"),
		},
		{name: "invalid (zero value)", in: netip.AddrPort{}},
		{name: "zoned IPv6", in: netip.MustParseAddrPort("[fe80::1%eth0]:3478")},
		{name: "zero port IPv4", in: netip.MustParseAddrPort("192.0.2.1:0")},
		{name: "unspecified IPv4", in: netip.MustParseAddrPort("0.0.0.0:3478")},
		{name: "unspecified IPv6", in: netip.MustParseAddrPort("[::]:3478")},
		{name: "multicast IPv4", in: netip.MustParseAddrPort("224.0.0.1:3478")},
		{name: "multicast IPv6", in: netip.MustParseAddrPort("[ff02::1]:3478")},
		{
			name: "unspecified IPv4 spelled as mapped IPv6",
			in:   netip.AddrPortFrom(netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff}), 3478),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalAddrPort(tt.in, canonicalStrict)
			assert.Equal(t, tt.strictOK, ok, "strict mode")
			if tt.strictOK {
				assert.Equal(t, tt.wantStrict, got, "strict mode result")
			} else {
				assert.Equal(t, netip.AddrPort{}, got, "strict mode rejects with zero value")
			}

			got, ok = canonicalAddrPort(tt.in, canonicalUnmap)
			assert.Equal(t, tt.unmapOK, ok, "unmap mode")
			if tt.unmapOK {
				assert.Equal(t, tt.wantUnmap, got, "unmap mode result")
				assert.False(t, got.Addr().Is4In6(), "unmap mode result is never mapped")
			} else {
				assert.Equal(t, netip.AddrPort{}, got, "unmap mode rejects with zero value")
			}
		})
	}
}

func TestCanonicalSourceAddr(t *testing.T) {
	tests := []struct {
		name string
		in   net.Addr
		ok   bool
		want netip.AddrPort
	}{
		{
			name: "IPv4 UDP source",
			in:   &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1).To4(), Port: 3478},
			ok:   true,
			want: netip.MustParseAddrPort("192.0.2.1:3478"),
		},
		{
			name: "IPv4-mapped 16-byte UDP source (dual-stack socket) unmaps",
			in:   &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478},
			ok:   true,
			want: netip.MustParseAddrPort("192.0.2.1:3478"),
		},
		{
			name: "IPv6 UDP source",
			in:   &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 3478},
			ok:   true,
			want: netip.MustParseAddrPort("[2001:db8::1]:3478"),
		},
		{name: "nil net.Addr", in: nil},
		{name: "nil *net.UDPAddr", in: (*net.UDPAddr)(nil)},
		{name: "TCP source", in: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478}},
		{name: "zoned UDP source", in: &net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 3478, Zone: "eth0"}},
		{name: "IPv4 UDP source with zone", in: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478, Zone: "eth0"}},
		{name: "zero port", in: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 0}},
		{name: "port above uint16", in: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 65536 + 3478}},
		{name: "negative port", in: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: -1}},
		{name: "nil IP", in: &net.UDPAddr{IP: nil, Port: 3478}},
		{name: "unspecified IPv4", in: &net.UDPAddr{IP: net.IPv4zero, Port: 3478}},
		{name: "unspecified IPv6", in: &net.UDPAddr{IP: net.IPv6unspecified, Port: 3478}},
		{name: "multicast IPv4", in: &net.UDPAddr{IP: net.IPv4(224, 0, 0, 1), Port: 3478}},
		{name: "multicast IPv6", in: &net.UDPAddr{IP: net.ParseIP("ff02::1"), Port: 3478}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalSourceAddr(tt.in)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			} else {
				assert.Equal(t, netip.AddrPort{}, got)
			}
		})
	}
}
