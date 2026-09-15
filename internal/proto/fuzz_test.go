// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package proto

import (
	"bytes"
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

type attr interface {
	stun.Getter
	stun.Setter
}

type attrs []struct {
	g       attr
	decoded attr
	t       stun.AttrType
}

func (a attrs) pick(v byte) struct {
	g       attr
	decoded attr
	t       stun.AttrType
} {
	idx := int(v) % len(a)

	return a[idx]
}

func FuzzSetters(f *testing.F) {
	// Include a valid value for each selector so every codec runs in the seed corpus.
	// Nonzero reserved bytes exercise canonicalization on the first encoding.
	for _, seed := range []struct {
		selector byte
		value    []byte
	}{
		{0, []byte{0x7f, 0xff, 0xff, 0xff}},
		{1, []byte{0, 0, 2, 88}},
		{2, []byte{0, 1, 0x12, 0x34, 192, 0, 2, 1}},
		{3, []byte("payload")},
		{4, []byte{0, 1, 0x56, 0x78, 192, 0, 2, 2}},
		{5, []byte{0}},
		{5, []byte{0x80}},
		{6, []byte{17, 0xff, 0xff, 0xff}},
		{7, []byte{}},
		{8, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{9, []byte{0x12, 0x34, 0x56, 0x78}},
		{10, []byte{1, 0xff, 0xff, 0xff}},
		{10, []byte{2, 0, 0, 0}},
		{2, []byte{0, 2, 0x12, 0x34, 0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
		{4, []byte{0, 2, 0x56, 0x78, 0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}},
		// IPv4-mapped IPv6 decodes to a 16-byte IP and re-encodes as IPv4.
		{2, []byte{0, 2, 0, 0, 0x21, 0x12, 0xa4, 0x42, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 0, 2, 1}},
		{4, []byte{0, 2, 0, 0, 0x21, 0x12, 0xa4, 0x42, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 0, 2, 2}},
	} {
		f.Add(seed.selector, seed.value)
	}

	f.Fuzz(func(t *testing.T, attrType byte, value []byte) {
		var (
			m1 = &stun.Message{
				Raw: make([]byte, 0, 2048),
			}
			m2 = &stun.Message{
				Raw: make([]byte, 0, 2048),
			}
			m3 = &stun.Message{
				Raw: make([]byte, 0, 2048),
			}
		)
		attributes := attrs{
			{new(ChannelNumber), new(ChannelNumber), stun.AttrChannelNumber},
			{new(Lifetime), new(Lifetime), stun.AttrLifetime},
			{new(XORPeerAddress), new(XORPeerAddress), stun.AttrXORPeerAddress},
			{new(Data), new(Data), stun.AttrData},
			{new(XORRelayedAddress), new(XORRelayedAddress), stun.AttrXORRelayedAddress},
			{new(EvenPort), new(EvenPort), stun.AttrEvenPort},
			{new(RequestedTransport), new(RequestedTransport), stun.AttrRequestedTransport},
			{new(DontFragment), new(DontFragment), stun.AttrDontFragment},
			{new(ReservationToken), new(ReservationToken), stun.AttrReservationToken},
			{new(ConnectionID), new(ConnectionID), stun.AttrConnectionID},
			{new(RequestedAddressFamily), new(RequestedAddressFamily), stun.AttrRequestedAddressFamily},
		}

		attr := attributes.pick(attrType)

		m1.WriteHeader()
		m1.Add(attr.t, value)
		if err := attr.g.GetFrom(m1); err != nil {
			require.NotErrorIs(t, err, stun.ErrAttributeNotFound)

			return
		}

		// All messages retain the same zero transaction ID for XOR addresses.
		m2.WriteHeader()
		require.NoError(t, attr.g.AddTo(m2))
		require.NoError(t, attr.decoded.GetFrom(m2))

		// Reserved bits and IP representations may normalize on encoding; compare
		// decoded semantics rather than requiring the original wire bytes back.
		switch original := attr.g.(type) {
		case *XORPeerAddress:
			decoded, ok := attr.decoded.(*XORPeerAddress)
			require.True(t, ok)
			require.True(t, original.IP.Equal(decoded.IP), "peer IP changed")
			require.Equal(t, original.Port, decoded.Port)
		case *XORRelayedAddress:
			decoded, ok := attr.decoded.(*XORRelayedAddress)
			require.True(t, ok)
			require.True(t, original.IP.Equal(decoded.IP), "relayed IP changed")
			require.Equal(t, original.Port, decoded.Port)
		default:
			require.Equal(t, attr.g, attr.decoded)
		}

		// A second encoding must preserve the first canonical encoding.
		m3.WriteHeader()
		require.NoError(t, attr.decoded.AddTo(m3))
		require.True(t, m2.Equal(m3), "canonical encoding changed")
	})
}

func FuzzChannelData(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0x40, 0, 0},                         // Short header.
		{0x40, 0, 0, 0},                      // Minimum channel, empty payload.
		{0x40, 0, 0, 1, 0x11},                // Unpadded payload.
		{0x50, 0, 0, 2, 0x11, 0x22},          // Above the old 20000 cutoff.
		{0x60, 0, 0, 3, 0x11, 0x22, 0x33, 0}, // Padded payload.
		{0x7f, 0xff, 0, 1, 0x44},             // Maximum channel.
		{0x3f, 0xff, 0, 1, 0x11},             // Below the valid range.
		{0x80, 0, 0, 1, 0x11},                // Above the valid range.
		{0x40, 0, 0, 2, 0x11},                // Truncated payload.
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		channelData := &ChannelData{Raw: bytes.Clone(data)}
		if channelData.Decode() != nil {
			return
		}
		require.True(t, channelData.Number.Valid())

		// Data can alias Raw: snapshot before Encode reuses its backing buffer.
		number := channelData.Number
		payload := bytes.Clone(channelData.Data)
		channelData.Encode()

		decoded := &ChannelData{Raw: channelData.Raw}
		require.NoError(t, decoded.Decode())
		require.Equal(t, number, decoded.Number)
		require.Equal(t, len(payload), decoded.Length)
		require.Equal(t, payload, decoded.Data)
	})
}

func FuzzIsChannelData(f *testing.F) {
	f.Fuzz(func(_ *testing.T, data []byte) {
		IsChannelData(data)
	})
}
