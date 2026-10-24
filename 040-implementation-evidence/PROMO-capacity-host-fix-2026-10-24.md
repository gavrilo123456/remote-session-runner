# PROMO controller fix — host-local capacity observations

**Result:** source correction and native capacity observations PASS. The owner's
full Dev preview retry and release activation remain NOT RUN by this task.

## Failure and cause

The owner's first `promote-to dev linux-host --release rr-20261024-43a4e675
--plan` stopped with `read-only lane backup-volume capacity measurement failed:
no such file or directory` before any promotion.

At predecessor source `b1d09b5f7742600a8be33125170b4ed881627ee3`,
`NativePromotionBackend.readLaneSnapshot` queried inventories through SSH but
then called `unix.Statfs` on the coordinator for the worker's backup path.
The Linux path `/home/ubuntu/.local/share/remote-session-runner-dev/backups`
does not exist on the Mac. A direct read-only SSH check proved it exists on
Linux as an `ubuntu`-owned mode-0700 directory, with 15,384,200 KiB available
on its filesystem. No directory creation or storage cleanup was needed.

## Correction

Source commit: `0002c057a3b5bbcd85d798925884993b8b4161a9`.

- Collect worker capacity through `runner-release host-capacity` using the
  existing verified SSH transport. The Mac measures its own destination locally.
- The helper verifies the host/platform/account and pinned clean controller,
  derives the fixed lane backup path and measures its filesystem read-only.
  Missing directories, symlinks and invalid/overflowing capacity values fail;
  it does not create paths or fall back to a different filesystem.
- Bind returned bytes to exact host, lane, backup path, controller revision and
  a fresh observation (within two minutes of coordinator time).
- Preserve the existing operating-buffer and separate PRD selector-reserve
  policy. Do not confuse filesystem observations with peer-lane identity.

No service bundle was rebuilt. The prepared release remains
`rr-20261024-43a4e675`, source `43a4e675ccccb89e1ea1c333e975ea028c9600fa`,
release digest `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`.
Controller source and candidate service source remain separate identities.

## Validation

Mac used the existing shared Go caches and Go 1.27.1:

```sh
go test ./src/internal/releaseflow -count=1
go vet ./src/internal/releaseflow ./src/cmd/runner-release ./src/cmd/promote-to
```

The complete affected-package suite passed (25.744 s); vet passed. Temporary
native builds of `runner-release` and `promote-to` passed help/version smoke.
Only task-owned smoke binaries were removed after exit.

The primary Gitea `origin/dev` and automatic GitHub mirror were verified at the
exact source commit above before clean `dev` fast-forwards on all three workers.
Each worker then passed, natively with Go 1.27.1 and `GOMAXPROCS=1 GOFLAGS=-p=1`:

```sh
go test ./src/internal/releaseflow -run Capacity -count=1
go vet ./src/internal/releaseflow ./src/cmd/runner-release ./src/cmd/promote-to
```

Both changed commands also passed native build/help/version smoke on every
worker. Linux capacity regressions took 0.168 s; Sandbox 0.108 s; Factory 0.096 s.
All three worker HEADs remained exactly `0002c057a3b5bbcd85d798925884993b8b4161a9`
with clean worktrees after validation. Full repository suites were not rerun
for this bounded controller correction; prior candidate qualification is
preserved rather than relabeled as a new run.

Regression coverage includes verified worker dispatch with distinct returned
capacities, wrong host/lane/path/controller, stale/future observations, trailing
JSON, insufficient capacity, missing paths without creation, symlink rejection,
preserved retained contents, foreign-platform/controller guards and
overflow checks. Fixtures use temporary paths; no installed lane is changed.

## Native read-only observations

The corrected helper returned successful observations for Dev, QAS and PRD on
all four hosts, using source commit `0002c05`. Example Dev results:

| Host | Observation UTC | Available bytes |
| --- | --- | ---: |
| Mac | 2026-10-24T14:53:18.856134Z | 133,648,891,904 |
| linux-host | 2026-10-24T14:54:01.594675991Z | 15,716,577,280 |
| sandbox-host | 2026-10-24T14:53:51.261046554Z | 1,168,060,416 |
| factory-sandbox | 2026-10-24T14:53:59.602820082Z | 34,154,635,264 |

These are filesystem snapshots, not deployment or backup acceptance. Actual
guards must remeasure before their writes. The owner will rerun the same
read-only promotion preview as the next walkthrough step.

No services were stopped/restarted; no selector, receipt, installed config,
database, mailbox request or ACK was changed. Retained work, backups, sealed
catalogs and all three preexisting untracked helper directories were preserved.
BUG-026 installed-release verification and Dev/QAS/PRD promotion remain pending.
