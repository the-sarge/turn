// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package turn

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the-sarge/turn/v5/internal/proto"
)

func TestAllocateRejectsForgedResponses(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)
	result := startObservedAllocate(cl, t.Context())
	authenticated := awaitAuthRequest(t, cl, conn)
	key := stun.NewLongTermIntegrity(testUsername, "test-realm", testPassword)
	for _, kind := range []string{"wrong method", "unsigned", "bad integrity"} {
		responseType := stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse)
		if kind == "wrong method" {
			responseType.Method = stun.MethodBinding
		}
		forged := stun.MustBuild(stun.NewTransactionIDSetter(transactionID(t, authenticated)), responseType,
			proto.RelayedAddress{IP: net.IPv4(192, 0, 2, 66), Port: 4444}, proto.Lifetime{Duration: time.Hour})
		switch kind {
		case "wrong method":
			require.NoError(t, key.AddTo(forged))
		case "bad integrity":
			require.NoError(t, stun.NewShortTermIntegrity("wrong key").AddTo(forged))
		}
		require.NoError(t, cl.HandleInbound(forged.Raw, testServerNetAddr()))
	}
	require.NoError(t, cl.HandleInbound(allocateSuccessResponse(t, authenticated), testServerNetAddr()))
	select {
	case outcome := <-result:
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.alloc)
		defer outcome.alloc.Close()
		assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:40000"), outcome.alloc.RelayedAddr())
	case <-time.After(time.Second):
		t.Fatal("valid authenticated Allocate did not complete")
	}
}

func TestPreparePeerRejectsForgedResponses(t *testing.T) {
	conn := newObserverConn()
	cl := newObservedClient(t, conn)
	alloc := completeObservedAllocate(t, cl, conn, 1, startObservedAllocate(cl, t.Context()))
	defer alloc.Close()
	ordinal := conn.recordedCount() + 1
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- alloc.PreparePeer(ctx, netip.MustParseAddrPort("192.0.2.2:9999")) }()
	key := stun.NewLongTermIntegrity(testUsername, "test-realm", testPassword)
	for _, method := range []stun.Method{stun.MethodCreatePermission, stun.MethodChannelBind} {
		raw := awaitWrite(t, conn, ordinal)
		request := &stun.Message{Raw: raw}
		require.NoError(t, request.Decode())
		require.Equal(t, method, request.Type.Method)
		var nonce stun.Nonce
		require.NoError(t, nonce.GetFrom(request))
		assert.Equal(t, "test-nonce", nonce.String(), "forged 438 must not change the shared nonce")
		for _, kind := range []string{"wrong method", "unsigned", "bad integrity", "unsigned stale nonce"} {
			responseType := stun.NewType(method, stun.ClassErrorResponse)
			code := stun.CodeForbidden
			if kind == "wrong method" {
				responseType.Method = stun.MethodBinding
			}
			if kind == "unsigned stale nonce" {
				code = stun.CodeStaleNonce
			}
			forged := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), responseType,
				stun.ErrorCodeAttribute{Code: code}, stun.NewRealm("test-realm"), stun.NewNonce("attacker nonce"))
			switch kind {
			case "wrong method":
				require.NoError(t, key.AddTo(forged))
			case "bad integrity":
				require.NoError(t, stun.NewShortTermIntegrity("wrong key").AddTo(forged))
			}
			require.NoError(t, cl.HandleInbound(forged.Raw, testServerNetAddr()))
		}
		valid := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID),
			stun.NewType(method, stun.ClassSuccessResponse), key)
		require.NoError(t, cl.HandleInbound(valid.Raw, testServerNetAddr()))
		ordinal++
	}
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("valid authenticated peer preparation did not complete")
	}
}

func TestRefreshRejectsForgedResponses(t *testing.T) {
	conn := newObserverConn()
	cl, err := NewClient(&ClientConfig{
		Conn: conn, Server: testServerAddrPort(), Username: testUsername, Password: testPassword, RTO: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(cl.Close)
	result := startObservedAllocate(cl, t.Context())
	authenticated := awaitAuthRequest(t, cl, conn)
	require.NoError(t, cl.HandleInbound(allocateSuccessResponseWithLifetime(t, authenticated, time.Second), testServerNetAddr()))
	var alloc *Allocation
	select {
	case outcome := <-result:
		require.NoError(t, outcome.err)
		alloc = outcome.alloc
	case <-time.After(time.Second):
		t.Fatal("Allocate did not complete")
	}
	defer alloc.Close()
	ordinal := conn.recordedCount() + 1
	refresh := awaitWrite(t, conn, ordinal)
	key := stun.NewLongTermIntegrity(testUsername, "test-realm", testPassword)
	for _, kind := range []string{"wrong method", "unsigned", "bad integrity", "unsigned stale nonce"} {
		responseType := stun.NewType(stun.MethodRefresh, stun.ClassSuccessResponse)
		if kind == "wrong method" {
			responseType.Method = stun.MethodAllocate
		}
		if kind == "unsigned stale nonce" {
			responseType.Class = stun.ClassErrorResponse
		}
		forged := stun.MustBuild(stun.NewTransactionIDSetter(transactionID(t, refresh)), responseType,
			proto.Lifetime{}, stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce},
			stun.NewRealm("test-realm"), stun.NewNonce("attacker nonce"))
		switch kind {
		case "wrong method":
			require.NoError(t, key.AddTo(forged))
		case "bad integrity":
			require.NoError(t, stun.NewShortTermIntegrity("wrong key").AddTo(forged))
		}
		require.NoError(t, cl.HandleInbound(forged.Raw, testServerNetAddr()))
	}
	require.NoError(t, cl.HandleInbound(refreshSuccessResponseWithLifetime(t, refresh, time.Minute), testServerNetAddr()))
	// The next periodic request observes the lifetime and nonce retained by the
	// real refresh consumer. A poisoned zero lifetime would instead release.
	nextRefresh := awaitWrite(t, conn, ordinal+1)
	requireRefreshLifetime(t, nextRefresh, time.Minute)
	request := &stun.Message{Raw: nextRefresh}
	require.NoError(t, request.Decode())
	var nonce stun.Nonce
	require.NoError(t, nonce.GetFrom(request))
	assert.Equal(t, "test-nonce", nonce.String())
	require.NoError(t, cl.HandleInbound(refreshSuccessResponseWithLifetime(t, nextRefresh, time.Minute), testServerNetAddr()))
}
