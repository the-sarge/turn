# the-sarge/turn

`the-sarge/turn` is GridSwarm's owned, single-purpose TURN client library. It provides the UDP allocation path used by wiremux and intentionally omits Pion TURN's server, TCP allocation, examples, and deployment surface.

The public package centers on `Client`: callers provide a `net.PacketConn`, allocate a UDP relay with `Client.Allocate`, and can use `Client.PrepareUDPPeer` to establish deterministic permission and channel-binding readiness before sending traffic.

## Install

Pin an explicit GridSwarm pre-release version:

```sh
go get github.com/the-sarge/turn/v5@v5.1.0-gs.1
```

The permanent `-gs` suffix keeps consumer upgrades deliberate. This repository is an owned library rather than an upstream-compatible fork; API removal and divergence are expected between milestone versions.

## Development

Run the ordinary local gate with `task verify` and the complete exact-head certification with `task preflight`.

Validation tool versions in `scripts/tool-versions.env` are owned by the repository maintainers. Review those pins weekly alongside the dependency-update queue and before every release, updating one tool family at a time and running `task preflight` after each change. The validation Go pin may advance independently of the consumer-facing `go` directive; raising that directive remains an explicit release decision.

This manual policy covers Go, Task, golangci-lint, govulncheck, actionlint, yq, and gitleaks. Retire it only when every pin has moved to an equivalently validated automated manifest.

Root integration tests use `github.com/pion/turn/v5@v5.0.12` only as a pinned test server fixture. That dependency is a transitional verification seam and is not imported by shipped code.

## License

MIT. See [LICENSE](LICENSE).
