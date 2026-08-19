# the-sarge/turn

`the-sarge/turn` is GridSwarm's owned, single-purpose TURN client library. It provides the UDP allocation path used by wiremux and intentionally omits Pion TURN's server, TCP allocation, examples, and deployment surface.

The public API is one idiom: callers provide a `net.PacketConn` and a canonical `netip.AddrPort` server address, allocate a UDP relay with `Client.Allocate(ctx)`, and receive an `*Allocation` — the one lifecycle object per allocation. `Allocation.PreparePeer(ctx, peer)` establishes deterministic permission and channel-binding readiness; after it succeeds, `Allocation.WriteTo` sends ChannelData over the confirmed binding (writes are prepared-only — there is no Send-indication fallback), `Allocation.ReadFrom` returns relayed datagrams with canonical `netip.AddrPort` sources, and `Allocation.Close` releases the relay. Failures are typed values (`ErrNotPrepared`, `ErrAllocationRefreshFailed`, `ErrChannelBindFailed`, and friends; closed-state errors match `errors.Is(err, net.ErrClosed)`); the library does not log.

## Install

Pin an explicit GridSwarm pre-release version:

```sh
go get github.com/the-sarge/turn/v5@v5.2.0-gs.1
```

The permanent `-gs` suffix keeps consumer upgrades deliberate. This repository is an owned library rather than an upstream-compatible fork; API removal and divergence are expected between milestone versions.

## Test fixture

The exported `turntest` package is a fork-owned, in-process scripted TURN responder covering exactly the request subset this client emits, with the knobs both this repository's and wiremux's integration tests need. It is a maintained verification aid with an example-level guarantee: it parses with `pion/stun` and `internal/proto`, hand-parses nothing, and claims no RFC conformance, relay quality, or interoperability. It must never be imported by non-test code.

## Development

Run the ordinary local gate with `task verify` and the complete exact-head certification with `task preflight`.

Validation tool versions in `scripts/tool-versions.env` are owned by the repository maintainers. Review those pins weekly alongside the dependency-update queue and before every release, updating one tool family at a time. Tools invoked by `task check` must support the consumer-facing Go version in `go.mod` because required CI installs that version; validate affected pin changes by running `task check` under that Go version with `GOTOOLCHAIN=local`, then run `task preflight` under the validation Go pin. The validation Go pin may advance independently of the consumer-facing `go` directive; raising that directive remains an explicit release decision.

This manual policy covers Go, Task, golangci-lint, govulncheck, actionlint, and gitleaks. Retire it only when every pin has moved to an equivalently validated automated manifest.

## License

MIT. See [LICENSE](LICENSE).
