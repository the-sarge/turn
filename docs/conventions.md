# Conventions

This guide records the six dimensions surveyed in September 2026. Observed preferences guide new work; they are not retroactive requirements or a reason for cosmetic rewrites. Linked ADRs own the documented contracts and their exceptions. Use [CONTEXT.md](../CONTEXT.md) for domain vocabulary.

## Error-message grammar

**Documented contract:** Root-defined sentinels carry the `turn:` prefix; the six re-exported sentinels retain their historical text and identities. Preserve causes with `%w` and classify with `errors.Is` or `errors.As`, rather than error-string matching. See the [admission and errors ADR](adr/2026-08-19-allocate-admission-errors-plan.md) and [kept API plan](adr/2026-08-15-modernize-kept-api-plan.md).

**Observed preference:** Descriptions are lowercase and add useful operation context. Historical sentinel wording is intentional; this preference does not authorize normalizing it.

## Panic/error boundary

**Documented contract:** Public nil contexts return errors; internal construction without the required abort capability panics as a programmer error. See the [admission and errors ADR](adr/2026-08-19-allocate-admission-errors-plan.md) and [construction crossing ADR](adr/2026-08-19-udpconn-construction-crossing-plan.md).

**Observed preference:** Invalid public inputs and operational failures return errors. Fuzz harnesses may panic to report failures, while ordinary tests use assertions; these serve different purposes.

## Naming and constructors

**Observed preference:** Use `NewX`/`newX` constructors, explicit configuration structs, and operations named for their behavior. `NewClient(*ClientConfig)`, `NewUDPConn(*AllocationConfig, abort)`, and the fixture's `New(Options)` have intentionally different crossings; uniform signatures and mutex names are not requirements.

**Documented contract:** Domain names come from [the glossary](../CONTEXT.md). The [construction crossing ADR](adr/2026-08-19-udpconn-construction-crossing-plan.md) bounds constructor helpers and rejects general builders/options layers for that seam. Its started-constructor behavior is superseded by the [publication and activation ADR](adr/2026-08-20-allocation-publication-activation-plan.md): `NewUDPConn` returns a quiescent connection and `UDPConn.Activate` publishes and arms timers.

## Tests and helper prefixes

**Observed preference:** Use `require` for prerequisites, `assert` for observations, named subtests, and `t.Helper` in helpers. Prefixes such as `new`, `confirmed`, and `start` describe the helper's actual job. Prefer explicit entry/completion signals and supplied times for ordering evidence; elapsed-time tests remain appropriate when latency is the requirement.

**Documented contract:** The [construction crossing ADR](adr/2026-08-19-udpconn-construction-crossing-plan.md) bounds scripted helpers to constructor-shaped jobs, preserves the real-Client close-latency harness, and permits the enumerated minimal-state literals used to control ordering. These exceptions do not call for a universal harness or uniform helper spelling. The [fixture ADR](adr/2026-08-15-fork-owned-test-fixture.md) bounds `turntest` to the client's emitted request subset.

## Script prologue

**Observed preference:** Executable scripts use `#!/usr/bin/env bash` and `set -euo pipefail`. Bash arrays and `[[ ... ]]` are appropriate. Existing `test` versus `[[ ... ]]` usage and placement of introductory comments are benign differences, not a mandate to rewrite scripts.

## Comment density and voice

**Observed preference:** Client comments explain ownership, invariants, cancellation, and locking; codec comments explain attributes and protocol context. Correct inaccurate facts and avoid duplicating mechanical narration. Uniform comment density is not a goal.

**Documented contracts:** [The transaction registry ADR](adr/2026-08-17-transaction-registry-plan.md) owns nonterminal `Client.Close`, retry ownership, and the distinction between Allocate cancellation and waiter-local PreparePeer cancellation. [The allocation lifecycle ADR](adr/2026-08-17-allocation-lifecycle-plan.md) owns sealing, release, joining, and caller-owned socket interruption. [The inbound delivery ADR](adr/2026-08-19-inbound-allocation-delivery-plan.md) preserves queued-before-seal data without promising which ready ReadFrom select arm wins. [The permission-attempt ADR](adr/2026-08-19-permission-owns-its-attempt.md) preserves the distinct permission and binding attempt owners. Keep these method-specific policies and ownership distinctions explicit instead of flattening them into one convention.
