// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package proto

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func BenchmarkChannelNumber(b *testing.B) {
	b.Run("AddTo", func(b *testing.B) {
		b.ReportAllocs()
		m := new(stun.Message)
		for range b.N {
			n := ChannelNumber(12)
			require.NoError(b, n.AddTo(m))
			m.Reset()
		}
	})
	b.Run("GetFrom", func(b *testing.B) {
		m := new(stun.Message)
		require.NoError(b, ChannelNumber(12).AddTo(m))
		for range b.N {
			var n ChannelNumber
			assert.NoError(b, n.GetFrom(m))
		}
	})
}

func TestChannelNumber(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		n := ChannelNumber(112)
		assert.Equal(t, "112", n.String())
	})
	t.Run("NoAlloc", func(t *testing.T) {
		stunMsg := &stun.Message{}
		allocated := wasAllocs(func() {
			// Case with ChannelNumber on stack.
			n := ChannelNumber(6)
			require.NoError(t, n.AddTo(stunMsg))
			stunMsg.Reset()
		})
		assert.False(t, allocated)

		n := ChannelNumber(12)
		nP := &n
		allocated = wasAllocs(func() {
			// On heap.
			require.NoError(t, nP.AddTo(stunMsg))
			stunMsg.Reset()
		})
		assert.False(t, allocated)
	})
	t.Run("AddTo", func(t *testing.T) {
		stunMsg := new(stun.Message)
		chanNumber := ChannelNumber(6)
		require.NoError(t, chanNumber.AddTo(stunMsg))

		stunMsg.WriteHeader()
		t.Run("GetFrom", func(t *testing.T) {
			decoded := new(stun.Message)
			_, err := decoded.Write(stunMsg.Raw)
			require.NoError(t, err)

			var numDecoded ChannelNumber
			err = numDecoded.GetFrom(decoded)
			require.NoError(t, err)
			assert.Equal(t, chanNumber, numDecoded)

			allocated := wasAllocs(func() {
				var num ChannelNumber
				assert.NoError(t, num.GetFrom(decoded))
			})
			assert.False(t, allocated)

			t.Run("HandleErr", func(t *testing.T) {
				m := new(stun.Message)
				nHandle := new(ChannelNumber)
				require.ErrorIs(t, nHandle.GetFrom(m), stun.ErrAttributeNotFound)

				m.Add(stun.AttrChannelNumber, []byte{1, 2, 3})
				assert.True(t, stun.IsAttrSizeInvalid(nHandle.GetFrom(m)))
			})
		})
	})
}

func TestChannelNumber_Valid(t *testing.T) {
	for _, tc := range []struct {
		n     ChannelNumber
		value bool
	}{
		{MinChannelNumber - 1, false},
		{MinChannelNumber, true},
		{MinChannelNumber + 1, true},
		{MaxChannelNumber, true},
		{MaxChannelNumber + 1, false},
	} {
		v := tc.n.Valid()
		assert.Equalf(t, tc.value, v, "unexpected: (%s) %v != %v", tc.n.String(), tc.value, v)
	}
}
