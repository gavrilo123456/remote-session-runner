# Existing-hosts three-lane scope reset — 2026-10-17

**State:** scope amendment and source-S delivery PASS.
No SIMPLE implementation, installed lane or live acceptance is claimed.

## Owner decision

The owner requires only the existing Mac and existing three Ubuntu workers,
each running independent Dev, QAS and PRD Runner instances in parallel. Use
current disks/accounts. PRD stays unsuffixed and pinned; Dev uses `-dev`, QAS
`-qas`. Additional OCI volumes, external Mac disks and the expanded
infrastructure programme are outside this assignment.

The authoritative [active profile](../040-improvments/040-dev-qas-prd-three-machine-lanes/025-existing-hosts-three-lane-scope.md),
active plan/handbook, AGENTS, ledger and handoff now express this scope.
The old advanced design, unit tables, implementation and results remain
historical. Unrun F tests are not PASS and are not SIMPLE prerequisites.
The A01 allocation request is withdrawn; no purchase or provisioning is made.

SIMPLE-01 through SIMPLE-07 define the current implementation sequence, all
NOT STARTED. The exact retained B021/B024 work and held ACKs remain preserved;
BUG-022/023 baseline work stays deferred and open. Current stable PRD may
remain installed when parallel-lane setup closes; a later production promotion
uses an explicit scoped maintenance with compatible backup/rollback.

## Baseline and checks

Pre-change Mac `dev`, primary Gitea `dev`, automatic GitHub mirror `dev` and
all three clean Ubuntu `dev` checkouts matched
`df9c8272473a3287cbf4ab36e60cc5ca16564242`. Mac's local `origin/dev` tracking
ref was stale; both actual remote refs were checked directly. Ubuntu `origin`
was the required GitHub mirror on each worker. Explicit SSH identities and
verified host keys were used under the existing owner authorization.

Mac validation:

- `git diff --check`: PASS.
- Review of active scope/sequence/ledger consistency and local links: PASS.
- Existing historical handbook-contract tests: PASS. Exact command:

```sh
# Mac, /Users/tomasz.walczuk/projects/remote-session-runner
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
GOTOOLCHAIN=local \
'/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' \
test ./src/internal/lanetests \
-run '^(TestLanesL101ManifestMatchesPlan|TestLanesL318PriorL3HermeticCoverageIsComplete|TestLanesL318ApprovalCheckpointsPreserveA01A06Order)$' \
-count=1
```

Only documentation changed. Full/race/build suites and runtime tests are not
required for this scope amendment and were NOT RUN. The focused tests verify
that preserving historical handbook tables does not break their existing
source contracts; they do not certify the active profile's implementation.

## Delivery and runtime boundary

Source S is `e7d49a33553399d566ee4d8a65190d2413451cce`.
The Mac committed only the ten intended documentation files. Its primary push
succeeded; the sandbox denied the local tracking-ref update, so actual remote
refs were checked directly rather than treating the stale tracking ref as
delivery failure. Primary Gitea and automatic GitHub mirror both returned S.
Each Ubuntu checkout was freshly verified clean on `dev` before its ff-only
pull; all then matched S and stayed clean. The three focused historical
handbook-contract tests and `git diff --check` passed on every worker at S.

| Surface | Verified revision | Result |
| --- | --- | --- |
| Mac `dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Intended documentation source commit; checks PASS |
| Gitea `origin/dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Primary push and direct ref verification PASS |
| GitHub `dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Automatic mirror exact-S verification PASS |
| `linux-host` `dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Clean ff-only delivery and 3 focused checks PASS |
| `sandbox-host` `dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Clean ff-only delivery and 3 focused checks PASS |
| `factory-sandbox` `dev` | `e7d49a33553399d566ee4d8a65190d2413451cce` | Clean ff-only delivery and 3 focused checks PASS |

Mac push and ref verification used the exact identity commands in AGENTS,
with `UpdateHostKeys=no`. Each Ubuntu pull used its explicit
`/home/ubuntu/.ssh/gavrilo123456-github` identity and `--ff-only origin dev`.
Worker test command, on each named Ubuntu host in the checkout:

```sh
# Ubuntu: linux-host, sandbox-host, factory-sandbox
git diff --check
GOTOOLCHAIN=local \
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go \
test ./src/internal/lanetests \
-run '^(TestLanesL101ManifestMatchesPlan|TestLanesL318PriorL3HermeticCoverageIsComplete|TestLanesL318ApprovalCheckpointsPreserveA01A06Order)$' \
-count=1
```

This evidence-only closeout E follows the same primary/mirror/clean-worker
delivery chain; its exact final hash and delivery are reported in the handoff,
not embedded in its own content. All SIMPLE implementation units remain
NOT STARTED; this PASS is the documentation correction only.

No services, installed configuration, cloud/storage,
firewall, credentials, mailbox requests, ACKs or retained work were changed.
Only tracked documentation and source-checkout Git delivery are in scope here.
