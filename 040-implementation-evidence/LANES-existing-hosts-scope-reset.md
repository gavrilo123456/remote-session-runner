# Existing-hosts three-lane scope reset — 2026-10-17

**State:** documentation/source checks PASS; delivery closeout IN PROGRESS.
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

Source S and its primary/mirror/three-worker delivery will be recorded in the
evidence-only closeout. No services, installed configuration, cloud/storage,
firewall, credentials, mailbox requests, ACKs or retained work were changed.
Only tracked documentation and source-checkout Git delivery are in scope here.
