# CAT-04 — Frozen candidate and live Dev qualification

**Status: IN PROGRESS; live activation and acceptance are NOT RUN.** The
candidate is sealed and published on all four hosts. No selector has changed,
and no Runner service has been stopped or restarted. Dev activation remains
held until admission fences and the complete qualification path can be proven.

## Candidate and source delivery

The immutable release is `cat-20261019-f0e2843`, built natively on each host
from source revision `f0e2843c5c92874ae817e7653fef01dd1311e5f6` with Go 1.27.1.
The exact shared `release.json` SHA-256 is
`aa3398575c46950c41e5279beb96b7c872bb8988e9203b34642584928ef4bba0`.
Compatibility declares reads for schemas 1–40 and writes for schema 40.

| Host | Immutable catalog path | Bundle SHA-256 | `catalog-inspect` |
| --- | --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261019-f0e2843/mac-control-plane` | `ff99ba5a3930a9c4d3e6b132426400405b7d89705920c40ed018f93c916576be` | PASS |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/linux-host` | `3da44d9b136e65c17f78326ebc0faf59e6053cdbb1df940b485b9b46035e1cb1` | PASS |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/sandbox-host` | `c46b888411cfc6aff4064807edfeeb27088ab0279bbbf6daeec66504c002c66b` | PASS |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/factory-sandbox` | `46950532a9efb9983937e0808ccfe1f9b4be3366d1bae9706e5437806d4d82fd` | PASS |

Each host's `catalog-inspect --host <host> --release-id cat-20261019-f0e2843`
returned the same release digest, the expected local host identity, source
revision and bundle digest. Publication is the only live filesystem addition
made for this unit; the mutable lane roots and their state remain in place.

Source revision `f0e2843` is on Mac `dev`, primary Gitea `origin/dev`, and the
automatic GitHub mirror. The three Ubuntu checkouts were clean on `dev` at the
exact mirrored revision before their native gates. At `f0e2843`, Mac and each
Ubuntu host passed `make test`, `make vet`, `make build`, and `make smoke`.
The Mac suite needed the authorized shell because the restricted invocation
could not inspect a test-owned process. An earlier Ubuntu test attempt at
`77ebf17` exposed old-Git incompatibility with `git init -b dev`; the fixture
was corrected, and the final full gates passed on all three hosts at
`f0e2843`. These earlier failures remain history and are not acceptance.

## Current live baseline

The four current Dev selectors remain legacy. Their bundle identities are:

| Host | Dev and QAS legacy bundle SHA | Running Dev/QAS build revision |
| --- | --- | --- |
| `mac-control-plane` | `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `linux-host` | `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `sandbox-host` | `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `factory-sandbox` | `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |

Read-only `catalog-status` confirmed PRD remains catalog-unselected on all
hosts. The existing unsuffixed PRD services are still running from their
legacy paths. The Mac's six Dev/QAS/PRD LaunchAgents and all nine Ubuntu
`runnerd`, `runnerd-dev`, and `runnerd-qas` services were observed active.
No service lifecycle or selector operation was attempted.

Supported private-socket `/health/ready` snapshots showed Dev and QAS ready
with build revision `00c1ca6e7694842c35d398583b3f26fea59e3613` on every
host. Reported active session slots, active command slots, queued commands,
queued intents, and mailbox backlog were zero. Mac dispatch-attempt baselines
were Dev 20 and QAS 13. `storage_errors_total` was zero except sandbox Dev and
QAS, both 2; `cleanup_failures_total`, event gaps, event lag, and truncation
counters were zero. The health response does not expose `resumable_jobs`, so
these snapshots are not the full restart guard and do not establish a fresh
quiescence PASS.

Sandbox `/` has 81 MiB available; `/dev/shm` has 2.9 GiB. Existing Dev and QAS
SQLite files are each below 1 MiB, but no maintenance backup has been created.
The small root-filesystem headroom must be rechecked immediately before any
backup or selector switch. No cleanup, cache deletion, or retained-work change
was made.

## Admission and qualification gates

The following required admission fences have not been recorded in a valid
cohort receipt:

| Scope | Current evidence |
| --- | --- |
| `mac-project-publishers` | The owner instruction not to resume publishers remains in force. Current control state and a bounded no-new-accepted confirmation were not independently verified. |
| `mac-ingress` | Mac Dev ingress service remains active; fence not applied. |
| `mac-queued-dispatch` | Mac Dev local executor remains active; fence not applied. |
| `linux-host-direct-ingress` | Dev service remains active; fence not applied. |
| `sandbox-host-direct-ingress` | Dev service remains active; fence not applied. |
| `factory-sandbox-direct-ingress` | Dev service remains active; fence not applied. |

Mailbox-directory counts and health mailbox-backlog gauges were not used as
publisher-pause proof. The exact owner action needed to proceed is a current,
verifiable pause confirmation for the external Dev publishers, naming their
controls and owners and establishing the no-new-accepted interval. After that
evidence is available, the supported lane stop/preflight path must independently
prove empty work on each host before creating the cohort hold and backup
receipts.

Installed candidate acceptance is **NOT RUN**: no candidate service was
started, no direct mTLS route probe or bounded execution probe was run, and no
queued bridge transaction was attempted. Mailbox request/ACK acceptance is
**NOT RUN** by instruction. The existing catalog root and old Dev/QAS
selectors are intact; QAS and PRD have not been promoted. No mailbox contents,
held ACKs, requests, sessions, capacity reservations, configuration, secrets,
database schema or workspaces were intentionally changed.

## Commands and machine scope

- **Mac source checkout:** `make test`, `make vet`, `make build`, and
  `make smoke` at `f0e2843`; build a temporary `runner-lane` executable and run
  `catalog-status`, `catalog-inspect`, and `plan`. Temporary output was removed
  after each process exited.
- **Each Ubuntu worker:** over normal SSH with its configured explicit
  identity and strict verified host key, build a temporary `runner-lane` from
  the clean synchronized `f0e2843` checkout; run `catalog-status` for Dev/QAS/PRD,
  `catalog-inspect` for the sealed candidate, and local-socket health checks.
  Temporary executables were created under `/dev/shm` and removed by shell
  traps. No tracked host files were edited.
- **Mac and Ubuntu hosts:** inspect service state, processes, free space, and
  Dev/QAS config/database file sizes. No service, selector, route, database
  schema, publisher, mailbox, or ACK mutation was performed.

## Delivery and next gate

The CAT-04 progress-only plan update is source commit `eea05f0a269fa511ea841e920190a2c5704d55cc`. Mac `dev`, primary Gitea `origin/dev`, and the automatic GitHub mirror all resolved to that exact commit. Each Ubuntu checkout was first verified clean on `dev` at `f0e2843`, then fast-forwarded from GitHub and verified clean at `eea05f0a269fa511ea841e920190a2c5704d55cc`. This Markdown-only commit is after the sealed candidate's source revision; the candidate stays pinned to `f0e2843`.

This evidence checkpoint does not close CAT-04. Do not begin CAT-05 QAS until
the whole Dev cohort has passed selector identity, readiness, direct and
queued route, lifecycle/teardown, capacity, and peer-preservation acceptance.
Do not prepare or execute CAT-06 PRD until CAT-05 QAS passes. CAT-07 remains
unstarted. The sealed `f0e2843` candidate remains outside Git and must not be
rebuilt during promotion; the Markdown-only progress/evidence commits do not
change its accepted inputs.
