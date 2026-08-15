# Integration tests use a fork-owned scripted TURN responder, not upstream's server

Track 1 deleted the server half and pinned upstream `github.com/pion/turn/v5@v5.0.12` as a test-only server fixture, a documented transitional seam. Wiremux's own test fixture turned out to be the fork's server at `v5.0.13-gs.1`, so every fork release after the cut breaks the consumer's tests until a replacement fixture exists. We decided (M1, Slice 5) to ship an exported `turntest` package — a scripted in-process responder covering exactly the request subset this client emits, with the knobs both repositories' tests need — and to drop the upstream module from `go.mod`, rather than have each repository carry an upstream test dependency forever.

## Considered options

- Upstream `pion/turn/v5` as a test-only dependency in both repositories: cheapest, but it moves the seam to the consumer instead of removing it, keeps a second TURN module in both test graphs, and ties deletion-observation evidence to upstream server internals.
- Delete real-server tests from the fork and rely on mock scenarios plus wiremux receipts: leaves the fork's own CI blind to wire regressions until a deliberate consumer bump.
- coturn in CI: an external service dependency in the fork's gate; wiremux certifies coturn interoperability separately.

## Consequences

- `turntest` is a maintained verification aid with an example-level guarantee: it parses with `pion/stun`/`internal/proto`, hand-parses nothing, and claims no RFC conformance or relay quality; it must never be imported by shipped code.
- Its supported request subset and knob list are enumerated in the M1 plan; growth beyond that list is a plan change, not a fixture tweak.
- Retirement: when both repositories certify against an external server in CI, or when a later track replaces it.
