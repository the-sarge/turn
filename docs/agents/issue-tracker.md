# Issue tracker: GitHub

Issues and PRDs for this repo live as GitHub issues in `the-sarge/turn`. Use the `gh` CLI for all operations.

**Caveat for this repo:** it is a GitHub fork of `pion/turn`, and `gh` resolves forks to their parent unless a default is set. Pass `--repo the-sarge/turn` explicitly on issue and PR commands (the clone also has `gh repo set-default the-sarge/turn` applied, but don't rely on it alone). Never create or edit issues against `pion/turn` from these skills.

## Conventions

- **Create an issue**: `gh issue create --repo the-sarge/turn --title "..." --body-file <path>`.
- **Read an issue**: `gh issue view <number> --repo the-sarge/turn --json number,title,body,labels,comments` so the body and labels come through with the comments.
- **List issues**: `gh issue list --repo the-sarge/turn --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --repo the-sarge/turn --body-file <path>`.
- **Apply / remove labels**: `gh issue edit <number> --repo the-sarge/turn --add-label "..."` / `--remove-label "..."`.
- **Sub-issue**: `gh issue create --repo the-sarge/turn --parent <parent> ...`, or `gh issue edit <parent> --repo the-sarge/turn --add-sub-issue <child>` afterwards. Without sub-issues, put `Part of #<parent>` at the top of the child body.
- **Blocking**: GitHub's native issue dependencies — `gh issue create --repo the-sarge/turn --blocked-by <n>,<n> ...` or `gh issue edit <child> --repo the-sarge/turn --add-blocked-by <blocker>`. Where dependencies aren't available, fall back to a `Blocked by: #<n>, #<n>` line at the top of the child body. A ticket is unblocked when every blocker is closed.
- **Count open blockers**: `gh api repos/the-sarge/turn/issues/<n> --jq .issue_dependencies_summary.blocked_by` (open blockers only; 0 means startable). `gh issue view --json blockedBy` also lists closed blockers.
- **Close**: post the explanation with `gh issue comment <number> --repo the-sarge/turn --body-file <path>`, then `gh issue close <number> --repo the-sarge/turn`. On an already-closed issue, `gh issue close --comment` drops the comment and still exits 0.

For multi-line bodies, write the exact Markdown to a file and use `--body-file`.

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --repo the-sarge/turn --json number,title,body,labels,comments` and `gh pr diff <number> --repo the-sarge/turn` for the diff.
- **List external PRs for triage**: `gh pr list --repo the-sarge/turn --state open --json number,title,body,labels,author,authorAssociation,comments` then keep only `authorAssociation` of `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE` (drop `OWNER`/`MEMBER`/`COLLABORATOR`).
- **Comment / label / close**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`, each with `--repo the-sarge/turn`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either — resolve with `gh pr view 42 --repo the-sarge/turn` and fall back to `gh issue view 42 --repo the-sarge/turn`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue in `the-sarge/turn`.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --repo the-sarge/turn --json number,title,body,labels,comments`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. `gh issue create --repo the-sarge/turn --label wayfinder:map`.
- **Child ticket**: a GitHub sub-issue of the map (see **Sub-issue** under Conventions). Where sub-issues aren't enabled, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is assigned to the driving dev.
- **Blocking**: see **Blocking** under Conventions.
- **Frontier query**: list the map's open children (scoped to the map's sub-issues / task list), drop any with an open blocker (see **Count open blockers**, or an open issue in the `Blocked by` line) or an assignee; first in map order wins.
- **Claim**: `gh issue edit <n> --repo the-sarge/turn --add-assignee @me` — the session's first write.
- **Resolve**: comment the answer, then close (see **Close** under Conventions), then append a context pointer (gist + link) to the map's Decisions-so-far.

Create any missing wayfinding labels when that workflow is first used.
