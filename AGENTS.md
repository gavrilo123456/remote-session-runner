# Repository workflow for Remote Session Runner

## Current lane assignment — existing hosts only

The owner's latest scope is authoritative: use the existing Mac and three
Ubuntu workers, with PRD unsuffixed, Dev `-dev`, and QAS `-qas` running in
parallel on each host. Follow
[`025-existing-hosts-three-lane-scope.md`](040-improvments/040-dev-qas-prd-three-machine-lanes/025-existing-hosts-three-lane-scope.md)
and the active `SIMPLE-01` through `SIMPLE-07` sequence in the phased plan.
The earlier advanced `LANES-*` rollout and its unmet native F gates are
superseded/out of scope, not PASS and not prerequisites of the SIMPLE track.

The owner assigned Factory-style release-catalog implementation CAT-01 through
CAT-07 on 2026-10-19. CAT-01 source, fixture, host source-check and delivery
gates are complete; CAT-03 legacy adoption, launcher and cohort integration is
active. Dev/QAS candidate actions follow their unit prerequisites, and CAT-06
PRD migration requires separate owner approval.
Follow the target
[catalog design](040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
and separate CAT-01–CAT-07 plan. Catalogs
belong under `~/projects-runtime/runner-releases/` on each existing host, with
independent lane selectors and unchanged writable runtime roots. No current
installation is migrated by source work; retain existing path guards and
commands until their supported CAT replacement is delivered.

Use ordinary directories on existing disks and the same existing users.
Keep each lane's binaries independently selected and separate its configuration,
DB, sockets, mailboxes, workspaces, logs, and keys. Keep installed PRD pinned independently of checkout
changes; installers, restarts, upgrades and rollback act only on the selected
lane. No new volumes, APFS provisioning, cgroup migration, reboot, dedicated
sshd, root release authority or management bootstrap is assigned. The former
A01 paid OCI/physical-Mac-disk package is withdrawn; no procurement or erasure
is authorized. Preserve existing PRD, shared caches and retained work.

## CAT-06 source-test assignment — owner amendment, 2026-10-22

For CAT-06, the owner authorizes the existing `factory-sandbox` Linux/AMD64
worker to supply the full source-suite result otherwise required from the
CPU-starved `linux-host`. Use the same candidate source revision and Go 1.27.1;
record platform/build settings and any coverage differences. Keep Mac and
Sandbox full-suite requirements unchanged. On `linux-host`, retain focused
CAT-06/changed-package regressions, the previously timed-out P025/P101 cases,
and native vet/build/smoke and applicable host-specific checks. Do not transfer
Factory results for checks that depend on Linux-host-only build settings,
kernel, service configuration or runtime ownership.

Keep previous Linux timeouts/interrupted suites recorded as NOT PASS. The
source gate is satisfied by the documented alternate-host assignment plus
passing native Linux checks, not by relabeling those failed runs. This is not
a waiver of four-host native bundles, retained-work/backup guards, or actual
Dev/QAS/PRD cohort acceptance. Do not rerun the starved host's full suite merely
to remove its historical status. Follow the current CAT-06 plan and evidence;
no new host or live service change is authorized by this test-plan amendment.

## Build and promotion CLI assignment — 2026-10-24

The owner assigned the source implementation in
[`030-build-release-and-promotion-cli-implementation-plan.md`](040-improvments/040-dev-qas-prd-three-machine-lanes/030-build-release-and-promotion-cli-implementation-plan.md), PROMO-01 through PROMO-07. The commands are `build-release`, `promote-to`, and `runner-release`; details and safe examples are in [`docs/promotion-guide.md`](docs/promotion-guide.md). Build and promotion workflow records stay under `~/projects-runtime/runner-release-operations/`; release bundles stay in the existing per-host catalog. Do not put credentials or live host configuration in Git.

PROMO source tests and four-host bundle construction are separate from
installed-lane acceptance. PROMO-08 is a separately instructed live walkthrough;
source implementation does not authorize service stop/start, selector changes,
mailbox publication, ACK publication, or live promotion. PRD still requires a
current release-bound owner approval before any future PRD mutation. Keep the
common selected-lane preparation scope visible even for a named-host selector
step, preserve peer lanes and retained work, and use the supported catalog
backup/receipt/selector/recovery commands rather than editing runtime state.

## Authoritative source checkout

Make all Runner source-code, test, schema, deployment-file, and project-documentation changes in the Mac checkout at `/Users/tomasz.walczuk/projects/remote-session-runner`. This checkout is the development source for the current PoC. Do **not** edit tracked project files directly in the Ubuntu checkout at `/home/ubuntu/projects/remote-session-runner`, and do not copy changed source files there with `scp` or `rsync`.

Host-specific secrets, service state, and runtime configuration belong outside the Git checkout. Never commit private keys or credentials. Host installation and tests may use the Ubuntu machine, but changes to versioned files must return through the Mac checkout and Git.

## Commit to primary, verify the mirror, then fast-forward the test hosts

For every completed change or implementation phase, use this order:

1. On the **Mac**, follow the applicable design and phased-plan gates, inspect the diff, and commit only the intended files on `dev`. Preserve unrelated or in-progress files in the worktree.
2. On the **Mac**, push that commit to the authoritative local Gitea `origin/dev` using `/Users/tomasz.walczuk/.ssh/local-gitea-key`. Verify the primary push succeeded; do not start remote validation against an unpushed primary commit.
3. Gitea automatically mirrors to GitHub. On the **Mac**, verify `github/dev` resolves to the exact primary commit. The mirror is a distribution path for the Ubuntu hosts; do not manually push it or treat it as source authority. If it has not caught up, stop and report the primary commit and observed mirror commit.
4. On each **Ubuntu** host, first verify `/home/ubuntu/projects/remote-session-runner` is on `dev` with a clean worktree. Its `origin` is the reachable GitHub mirror, so pull `origin/dev` with `--ff-only` using `/home/ubuntu/.ssh/gavrilo123456-github`. Verify the resulting Ubuntu `HEAD` equals the verified primary and mirror commit before building or testing that revision.
5. Record the Mac commit, primary Gitea branch, verified GitHub mirror commit, every required Ubuntu commit, commands, and test results in the phase evidence. Do not call a phase delivered on Ubuntu or start the next phase until the required primary push, mirror verification, pulls, and validation are complete.

The Mac primary remote is `ssh://local-gitea/myorganization/remote-session-runner.git`; it resolves locally to Gitea on `localhost:2222`. The Mac `github` remote is `git@github.com:gavrilo123456/remote-session-runner.git` and is a secondary automatic mirror. The three required Ubuntu profiles are `linux-host`, `sandbox-host`, and `factory-sandbox`; each `origin` points to that GitHub mirror because it cannot reach the Mac-local Gitea endpoint. Use explicit SSH identities; no checkout should depend on an agent or a default SSH key:

```sh
# Mac, in /Users/tomasz.walczuk/projects/remote-session-runner
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Mac: wait for the automatic GitHub mirror to contain the same commit.
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Ubuntu, in /home/ubuntu/projects/remote-session-runner, after checking branch and status
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

If any required checkout is dirty, the primary/mirror commits differ, authentication fails, or a pull is not fast-forwardable, stop and report the exact state. Do not force-push, reset, overwrite local files, bypass the primary Gitea handoff, or skip mirror verification. The serial phase/test requirements in the applicable detailed phased implementation plan still apply. For Dev/QAS/PRD lane work, use the active `SIMPLE-*` sequence and evidence/closeout rules in `040-improvments/040-dev-qas-prd-three-machine-lanes/015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md`, its scope addendum, handbook and execution ledger; preserve historical `LANES-*` and `Pnnn` evidence without relabeling unrun work PASS.

Resolve actual access, capacity, producer and change dependencies before the
operation that needs them. Independent source work may proceed while an
unrelated live operation is blocked; record the blocker without implying PASS.
Keep current B021/B024 retained work and held ACKs preserved within their
existing exact scope; they are not a blanket prerequisite for independent
SIMPLE source work. BUG-022/023 remain deferred and OPEN. Record new standalone
bugs without automatically assigning a broader baseline repair campaign.
Corrections directly needed by an active SIMPLE unit and its tests remain
required. Preserve selected-lane acceptance, Git delivery, quiescence,
ownership and explicit operational approvals, including the scoped PRD upgrade.

## Go cache and temporary files

On the Mac, use the shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and `GOMODCACHE=/private/tmp/remote-session-runner-gomodcache` configured by the Makefile. Reuse these across phases; do not create a new cache or module-cache copy per phase. On Ubuntu, reuse the selected account's normal Go caches. Use `t.TempDir()` for test fixtures and shell `trap` cleanup for temporary host scripts. Keep generated build/test output outside the Git checkout, and remove only task-owned temporary artifacts after their processes have exited. Do not clean shared or other-project caches.
