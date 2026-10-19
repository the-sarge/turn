// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package client

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // STUN MESSAGE-INTEGRITY uses HMAC-SHA1.

	"github.com/pion/stun/v3"
)

// responsePolicy is an immutable snapshot of one request's authentication.
type responsePolicy struct {
	method    stun.Method
	integrity stun.MessageIntegrity
	realm     []byte
}

func (p responsePolicy) accepts(msg *stun.Message) bool {
	if msg.Type.Method != p.method ||
		(msg.Type.Class != stun.ClassSuccessResponse && msg.Type.Class != stun.ClassErrorResponse) {
		return false
	}
	if len(p.integrity) == 0 {
		return msg.Type.Class == stun.ClassErrorResponse && hasChallenge(msg, stun.CodeUnauthorized, nil)
	}

	for i, attr := range msg.Attributes {
		if attr.Type != stun.AttrMessageIntegrity {
			continue
		}
		// Check's HMAC offset assumes the fixed SHA-1 attribute size. Reject
		// malformed lengths before handing untrusted input to that routine.
		if attr.Length != sha1.Size || p.integrity.Check(msg) != nil {
			return false
		}
		// Attributes after MESSAGE-INTEGRITY are outside its HMAC. Hide them
		// from operation consumers, as required by RFC 8489 section 6.3.
		msg.Attributes = msg.Attributes[:i+1]
		if msg.Type.Class == stun.ClassErrorResponse {
			var code stun.ErrorCodeAttribute
			if code.GetFrom(msg) == nil && code.Code == stun.CodeStaleNonce {
				return hasChallenge(msg, stun.CodeStaleNonce, p.realm)
			}
		}

		return true
	}

	return false
}

func hasChallenge(msg *stun.Message, expected stun.ErrorCode, realm []byte) bool {
	var code stun.ErrorCodeAttribute
	if code.GetFrom(msg) != nil || code.Code != expected {
		return false
	}
	gotRealm, realmErr := msg.Get(stun.AttrRealm)
	nonce, nonceErr := msg.Get(stun.AttrNonce)

	return realmErr == nil && nonceErr == nil && len(gotRealm) > 0 && len(nonce) > 0 &&
		(realm == nil || bytes.Equal(gotRealm, realm))
}
