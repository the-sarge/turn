# Permission owns its attempt; channel binding does not

Channel-binding readiness is a time-based, multi-state machine (idle, request, unknown, ready, refresh, failed, with confirmation age, prepared history, and expiry), so the 2026-08-19 readiness track kept its durable readiness in `binding` and its attempt coordination — the in-flight attempt handle and its lock — on `UDPConn`. Permission has no durable machine to separate: its readiness *is* the outcome of one CreatePermission attempt (idle → attempting → permitted, or failed and removed). We decided (2026-08-19 seam deepening program, Track 3) that `permission` owns its attempt lifecycle and permitted fact behind intent-level operations with one private lock, while `UDPConn` keeps worker registration, the transaction and retry, seal precedence, map deletion, and refresh disposition. The resulting asymmetry between the two readiness modules is deliberate.

## Considered options

- Mirror binding's split for permission (durable readiness in `permission`, attempt handle on `UDPConn`): leaves a one-bit bag and keeps the attempt protocol in the caller — the shallow shape being removed.
- Relocate binding's attempt handle and lock to match permission (into `binding`, or into a `UDPConn`-side map): a relocation with no demonstrated cost and no deletion-test signal; the readiness plan placed it deliberately. Rejected; do not re-suggest without a concrete lock-ownership defect.
- One shared attempt helper or handle for both: closed without code by the 2026-08-19 program (attempt coalescing) because the two paths differ in map deletion, eligibility, transitions, worker rollback, and disposition.

## Consequences

- A reader will find the attempt handle on `permission` and on `UDPConn` for binding; this ADR is the reason.
- Neither module may grow a shared attempt abstraction. If permission ever needs expiry or refresh eligibility of its own, revisit this decision rather than bolting a second machine onto the attempt.
