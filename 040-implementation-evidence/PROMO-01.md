# PROMO-01 — command and record foundation

**Status:** PASS — implementation and fixture qualification; no live effect.

- Source implementation: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- Git fixture portability correction: `43a4e675ccccb89e1ea1c333e975ea028c9600fa`.
- Added the `build-release`, `promote-to`, and `runner-release` entry points,
  typed records, owner-only host configuration validation, operation storage,
  locks, CLI help/exit semantics, and explicit whole-lane effects in plans.
- Public input, plan-only, invalid-scope, record-integrity, and subprocess
  cases pass in `go test ./...` on Mac at the source content represented by
  `43a4e67`; host-specific results are summarized in [PROMO-07](PROMO-07.md).
- No services, selectors, databases, mailbox files, or host configuration were
  changed. No release artifact was created.

## Delivery

The implementation commit and portability correction were delivered through
Mac `dev` → primary Gitea → exact GitHub mirror → clean worker fast-forwards.
This evidence set is delivered through the same required sequence; its resulting
commit is reported in the completion handoff.
