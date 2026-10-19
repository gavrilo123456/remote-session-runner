# Factory-style Runner catalog — design alignment evidence

## Assignment and outcome

On 2026-10-19 the owner selected **update design and plan first**, rather than
implementing or deploying the release catalog. The target is
`~/projects-runtime/runner-releases/<release-id>/<host-id>/` on each existing
host, with sealed cohort identity and independent Dev/QAS/PRD selectors.
Configuration, databases, mailboxes, sockets, credentials, workspaces, logs and
backups stay at their existing lane locations. No runtime migration is claimed.

The [idea](../040-improvments/070-factory-style-runner-releases/005-initial-idea.md),
[detailed design](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
and [CAT plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md)
define immutable native bundles, cohort/version identities, publication/path
validation, legacy adoption, backward-compatible current-data rollback and
retained-version protection. All CAT-01–CAT-07 units remain NOT STARTED.

Active lane scope/plan/handbook, AGENTS, execution-ledger planning notes and the
lane/PRD runbooks now link the target while distinguishing it from supported
legacy behavior. Stale blanket claims that no PRD tool exists were corrected
to reference the already implemented separate `prd-*` maintenance path. No live
service revision or historical phase status was updated from inference.

## Review and validation

Two independent read-only reviews identified the same material gap: Factory's
single-Mac version switch does not define a distributed Runner mixed-version
transition. The design/plan now require a whole selected-lane hold, worker-first
then Mac order, per-host/cohort receipts, scoped maintenance probes and no normal
resumption until all four target identities/routes pass. A failure after any
host switch keeps the cohort held for qualified completion/recovery. Future
rolling availability needs explicit old/new transport compatibility evidence.

On Mac, Python structural checks passed for the eleven changed/new planning and
runbook documents: 111 relative file links resolved, fences/newlines were valid,
CAT-01–07 row IDs were unique and ordered, and cohort/layout/scope contracts
were consistent. `git diff --check` passed. This evidence file is also checked
for links and formatting before delivery. No executable product test, source
build, fixture publication, database opening, service action, mailbox request
or ACK was performed for the design assignment.

## Source and delivery

Initial Mac `dev` was clean at
`9358150f1abae2dc7bb2aec8fdea9db14b62f809`, one documentation commit ahead of
primary Gitea and the automatic GitHub mirror, both at
`b46a72068a973d94a22fbeaeb3bd853ae8b5c7e4`. The existing unpushed commit
registers configurable logging and is preserved unchanged. All three Ubuntu
checkouts were clean `dev` at `b46a720` with the expected GitHub origin.

Design-only commit **S** is
`dde1b17b135b5b3ef268f236649065ba73c421a4`. Mac `dev` was clean after committing
the twelve intended documentation files. The primary Gitea push completed;
explicit-identity `git ls-remote` returned exactly S for both primary `origin/dev`
and automatic GitHub `dev`. The earlier logging-proposal commit was delivered
unchanged as S's existing ancestor.

Each Ubuntu checkout was freshly verified clean on `dev` with its expected
GitHub origin, then ran explicit-identity `git pull --ff-only origin dev` and
reached S. `linux-host`, `sandbox-host` and `factory-sandbox` all remained clean.
Each passed `git diff --check` and Python validation of all twelve documents,
114 relative links, fences/newlines, CAT-01–07 row order and design-only/cohort
hold markers. Mac passed the same structural checks. No executable suite or
live acceptance was required or performed for these Markdown changes.

The evidence-only commit containing this receipt follows the same primary,
exact automatic-mirror and clean-worker fast-forward delivery. Its final SHA
is reported after verification rather than embedded as a self-reference. No
CAT implementation/unit completion or installed acceptance follows from source
synchronization; all seven units remain NOT STARTED.
