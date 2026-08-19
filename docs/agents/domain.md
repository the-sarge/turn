# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

**Layout: single-context.** This repo is one Go module with one purpose (the UDP TURN client plus the exported `turntest` fixture); `CONTEXT-MAP.md` is not used.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root.
- **`docs/adr/`** — read ADRs that touch the area you're about to work in. Files are dated `YYYY-MM-DD-<slug>.md` and include both decision records (e.g. `2026-08-14-owned-library-fork.md`, `2026-08-15-prepared-only-writes.md`) and program/plan documents.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The `/domain-modeling` skill (reached via `/grill-with-docs` and `/improve-codebase-architecture`) creates them lazily when terms or decisions actually get resolved.

## File structure

```
/
├── CONTEXT.md                 ← not yet created; /domain-modeling creates it lazily
├── docs/adr/
│   ├── 2026-08-14-owned-library-fork.md
│   ├── 2026-08-15-prepared-only-writes.md
│   └── ...                    ← dated files: YYYY-MM-DD-<slug>.md
├── notes/                     ← working grill context, not the glossary
├── turntest/                  ← exported test fixture (same context)
└── *.go                       ← flat single Go module
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR `2026-08-15-prepared-only-writes` (prepared-only writes) — but worth reopening because…_
