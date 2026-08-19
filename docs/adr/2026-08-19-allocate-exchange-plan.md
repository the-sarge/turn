# Allocate Exchange Implementation Plan

**Date:** 2026-08-19
**Status:** Accepted; not yet implemented
**Track:** 4 of 4 in the 2026-08-19 seam deepening program
**Depends on:** Nothing — parallel-safe with every other track (root-only)
**Related:** [Program index](2026-08-19-seam-deepening-program.md), [Modernize kept API plan](2026-08-15-modernize-kept-api-plan.md), [Server-bound transport plan](2026-08-19-server-bound-transport-plan.md), [Allocation construction timing validity plan](2026-08-19-allocation-construction-timing-validity-plan.md), behaviour decision [#83](https://github.com/the-sarge/turn/issues/83)
**Normative scope:** Current outcome, boundaries, invariants, acceptance evidence, blockers, and stop conditions
**Audit history:** Self-grilled and independently slice-audited against `dad68d4868efc8b041114f7a4efdeaa3b229edfb` on 2026-08-19 (see the program index for the audit receipt)

## Goal

Make the Allocate exchange one module: it builds both Allocate requests from one setter list, derives the long-term credentials from the 401 challenge, and returns everything the Allocation needs as one value — so `Client` stops carrying credentials as mutable fields that a reader must know to read back, the emitted bytes are asserted at the wire instead of through three single-caller helpers, and the request construction cannot drift between the anonymous and authenticated forms. Emitted bytes and setter order are pinned, not changed; the protocol-ordering fact the wire test reveals is a separate behaviour decision (#83).

## Current Shape (verified 2026-08-19 at `dad68d4868efc8b041114f7a4efdeaa3b229edfb`)

Address-family selection is three single-caller pure functions — `inferAddressFamilyFromConn`, `getRequestedAddressFamily`, `appendRequestedAddressFamily` (`client.go:84-130`) — tested in isolation (`client_test.go:519-592`; the first two with real `udp4`/`udp6` listeners, the third by building messages without a socket). The inference fallback arm (`client.go:96-97,111`) is uncovered. Nothing asserts the attribute on an emitted datagram.

`sendAllocateRequest` (`client.go:181-273`) writes the Allocate setter list twice: anonymous `[TransactionID, Type, RequestedTransport, (family), Fingerprint]` (`client.go:187-196`) and authenticated `[TransactionID, Type, RequestedTransport, Username, Realm, Nonce, Integrity, (family), Fingerprint]` (`client.go:222-235`). In the authenticated list `REQUESTED-ADDRESS-FAMILY` follows `MESSAGE-INTEGRITY`; pion/stun computes the integrity HMAC over the message up to the attribute preceding MESSAGE-INTEGRITY, and RFC 8489 §14.5 requires servers to ignore attributes after it other than FINGERPRINT. That ordering is inherited from upstream, unobserved by any test (`turntest` never reads the attribute), and is the subject of #83. The latent inference behaviour — `a.IP.To4() != nil` classifies a wildcard `::` socket as IPv6 — is likewise unobserved.

Credentials are a hidden output: `sendAllocateRequest` returns `nonce` but writes `c.realm` and `c.integrity` onto `Client` fields documented `// Read-only` (`client.go:66-67,217-220`), which `Allocate` then reads back into `AllocationConfig` (`client.go:320-323`). No reuse across Allocations exists: a second Allocate after Close starts anonymous and re-derives them.

`observerConn` already records exact outbound datagrams and scripts the 401 challenge (`allocate_ctx_test.go:30-110,153-199`); its `LocalAddr()` is hard-coded to `127.0.0.1:5555` (`allocate_ctx_test.go:94-96`). Production requests use `stun.TransactionID` (random), and MESSAGE-INTEGRITY and FINGERPRINT cover the header, so recorded bytes cannot be replayed literally; the repository's existing technique is txid-normalized equality — build the expected message from an explicit setter list with `stun.NewTransactionIDSetter(actual.TransactionID)` and compare `Raw` (`internal/client/udp_conn_test.go:194-213`).

## Decision

`sendAllocateRequest` becomes the Allocate exchange: it builds the anonymous request, performs the cancelable transaction, reads the 401 challenge's realm and nonce, derives the long-term integrity, builds the authenticated request, performs it, and returns one unexported value — relayed address, lifetime, realm, nonce, integrity — or an error. Both requests come from one setter builder that emits exactly today's order for each form, including today's placement of `REQUESTED-ADDRESS-FAMILY` (after `MESSAGE-INTEGRITY` in the authenticated form, before `FINGERPRINT` in both) and `FINGERPRINT` last. `Client` drops the mutable `realm` and `integrity` fields; `username` stays (config-derived, read-only). `Allocate` copies the returned credentials into the existing `AllocationConfig` fields — no `AllocationConfig` change. Address-family inference collapses to one unexported function computed once in `NewClient`, with behaviour unchanged (including the `::` classification).

The wire is the test surface: a characterization table over recorded datagrams — socket family {IPv4, IPv6, non-`*net.UDPAddr` local address fallback} × request form {anonymous, authenticated} — asserts attribute presence/absence and position, `FINGERPRINT` last, and byte-identity after normalizing to the observed transaction ID: for each cell the test builds the expected message from an explicit, hard-coded setter list with `stun.NewTransactionIDSetter(actual.TransactionID)` and the scripted realm/nonce, and compares `Raw`; MESSAGE-INTEGRITY and FINGERPRINT then match iff order and values match. `observerConn` gains a settable local address (no other knob) to drive the IPv6 and fallback cells. The three helper tests are deleted. The slice does not move the attribute; if #83 decides to move it, that is a one-line change to the builder and an intentional update of the pinned bytes in its own PR.

**Rejected alternative (do not do this):** Fix the post-integrity placement inside this slice because the test makes it visible. Every program rule pins emitted bytes and setter order; moving the attribute is a wire change that needs #83's decision and its own PR.

**Rejected alternative (do not do this):** Keep `realm`/`integrity` on `Client` "for reuse." No reuse exists; the fields are carriers between two lines of one method.

**Rejected alternative (do not do this):** Generalize into an authenticated-exchange module shared with Refresh/CreatePermission/ChannelBind. Closed by the 08-17 program; this stays inside Allocate.

**Rejected alternative (do not do this):** Change the `::` inference, default family, or RFC 6156 semantics. Reported, not changed.

**Non-goals:** No wire-byte or setter-order change; no `AllocationConfig` change; no change to the 401 flow, nonce handling, cancellation semantics, or `performAllocateTransaction`; no `turntest` change; no IPv6 relay behaviour change.

## Slice Graph

| Slice | Status/disposition | Delivers | Blocked by | Removes temporary seam |
|---|---|---|---|---|
| T4.S1 | New | One Allocate exchange returning one value; one setter builder; credentials off `Client`; wire-asserted characterization table replacing three helper tests | None | Duplicate setter lists; hidden credential output |

## Implementation Slices

### Slice T4.S1 — One Allocate exchange, wire-pinned

**What it delivers:** One PR pinning the current anonymous and authenticated Allocate shape for each socket-family class as txid-normalized characterization tests, then refactoring `sendAllocateRequest` to one setter builder and one returned value, deleting `Client.realm`/`Client.integrity` and the three family helpers (inlined to one function), and deleting the three helper tests.

**Existing-work disposition:** New slice.

**Blocked by:** None.

**Single owner after merge:** The Allocate exchange is the only builder of Allocate requests and the only deriver of long-term credentials; `NewClient` is the only place address family is inferred; `Allocate` is the only consumer of the returned value and the one production assembler of `AllocationConfig`.

**Authority completeness:** No persisted fact. Both request forms, all three socket-family classes, the 401 challenge, and the single consumer are covered.

**Transitional-seam budget:** None at merge. No second setter list, no mutable credential field on `Client`, no family helper beyond the one function.

**Blast radius:** `client.go:58-130,181-273,310-328`; `client_test.go:519-589` (deleted) and `allocate_ctx_test.go` (extended). Wire: bytes and order identical — pinned by txid-normalized equality tests written against the current code before the refactor. Concurrency: none (Allocate is serialized by admission; the returned value removes writes to `Client` fields during Allocate). Interfaces: no public change. Failure modes: every existing attribute-missing arm of the response parse is preserved. Performance/security/dependencies: none. Untraced effects are not accepted: any byte difference in any of the six cells stops the slice.

**Artifact classification:** The exchange and builder are shipped behavior. The characterization table is a verification aid; it is not a maintained product deliverable beyond this repository's ordinary test suite. #83 is process metadata.

**Representation contract:** Supported domain = the two Allocate request forms × three socket-family classes (IPv4 UDP, IPv6 UDP, non-`*net.UDPAddr` local address) with the observed transaction ID and scripted credentials. Representation owner = the exchange's setter builder; pion/stun owns STUN encoding. Guarantee = universal over those six cells (byte-identical), example-level for server responses (the scripted 401 and success). Terminating evidence = the six txid-normalized cells, the existing Allocate/cancellation suites, `go test -race ./...`, `task preflight`, hosted CI, one review plus at most one replacement.

**Contract closure:** Not triggered — the invariant (byte-identical requests) has one owner and a finite six-cell domain that the focused table covers directly.

**Evidence budget:** Six txid-normalized byte-equality cells (expected setter lists written against the current code before the refactor, scripted realm/nonce); attribute-position assertions (`REQUESTED-ADDRESS-FAMILY` absent for IPv4 and fallback, present for IPv6 at today's position; `FINGERPRINT` last); one test that the returned value carries realm, nonce, and the derived integrity for a scripted 401; existing `TestAllocateTargetsConfiguredServer`, cancellation, and `turntest` E2E suites; `go test -race ./...`. No guard mutation (no enforcement guard). Negative evidence: no `Client.realm`/`Client.integrity` field; no `inferAddressFamilyFromConn`/`getRequestedAddressFamily`/`appendRequestedAddressFamily` symbol. No new platforms, repetitions, or timing.

**TDD and preservation evidence:** Write the six txid-normalized cells and the position assertions first against the current code (they must pass before any refactor), extending `observerConn` with a settable local address; then introduce the builder and the returned value; then delete the helpers, fields, and helper tests. Preservation: the six cells are the gate.

**Dispatch context budget:** This slice contract; `client.go:58-130,181-273,285-331`; `allocate_ctx_test.go:30-251` (extend `observerConn` with a settable `LocalAddr`, no other knob; reuse `assertRequestShape`'s technique from `internal/client/udp_conn_test.go:194-213`); `client_test.go:519-592`; #83 for context only. Root-only; no `internal/client` change; `sendAllocateRequest` overlaps T1.S2's `trRes.Msg` edits (rebase). Fits one fresh context.

**Slice decision audit:** Strongest further split = land the characterization tests first as their own PR. Rejected: a test-only PR that pins bytes nobody is changing has no independent payoff; the tests are the first commit of this PR. Strongest merge = fold #83's fix in. Rejected: wire change needs its own decision. No blocking edge.

**Stop conditions:** Any of the six cells differs after the refactor; the builder cannot reproduce both orders from one list without a flag that itself needs documenting (then keep two explicit lists inside the builder and stop widening); or a consumer of `Client.realm`/`integrity` outside Allocate appears.

## Acceptance Criteria

- [ ] Anonymous and authenticated Allocate requests are byte-identical, after transaction-ID normalization, to the pre-change shape for IPv4, IPv6, and fallback sockets; `REQUESTED-ADDRESS-FAMILY` is present only for IPv6 at today's position and `FINGERPRINT` is last. Domain: the six cells; owner: the exchange's builder; guarantee: universal over the six cells; evidence: the txid-normalized equality table.
- [ ] `sendAllocateRequest` returns relayed address, lifetime, realm, nonce, and integrity as one value; `Client` has no `realm`/`integrity` field; `AllocationConfig` is unchanged.
- [ ] One unexported family-inference function computed once in `NewClient`; the three helpers and their tests are gone; inference behaviour is unchanged.
- [ ] #83 remains open and the attribute placement is unchanged by this slice.

## Validation Gates

The txid-normalized/position table, the returned-value test, existing Allocate and `turntest` suites, `go test ./...`, `go test -race ./...`, `task preflight`; draft through review and certification, ready, post-ready `ci-required` success on the exact head, squash merge.

## Operating Discipline

Follow the shared review-loop and contract-closure baselines supplied by `$implement-architecture-slice`; this repository has no overlay. Bytes are pinned; #83 owns any change to them. Stop rather than generalizing beyond Allocate.
