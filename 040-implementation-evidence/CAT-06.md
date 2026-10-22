# CAT-06 — PRD adoption and promotion

## CAT-06 checkpoint — 2026-10-22 07:56 UTC — compatibility inputs validated; Linux full suite interrupted; PRD remains blocked

Revision `9113b44fc436014c4017522ca2c2432ed66c79b9` remains the clean source
revision on Mac `dev`, primary Gitea, the exact GitHub mirror, and all three
Ubuntu `dev` checkouts. The untracked `src/cmd/catalog-holdcheck/` remains
untouched. The source contains the public-command subprocess harness and
read-only compatibility-input checker.

The corrected Linux P025 test and the previously slow P101 test both pass at
this revision. One full `make test` attempt started at 07:46:04 UTC; the
command packages and `runner-lane` passed, but no further package output
arrived before host load reached 19.67 and CPU steal 93%. SIGINT was sent only
to task-owned Go PID 4133338. The make target exited 2 and remains
**INTERRUPTED / NOT PASS**. No assertion failure is established. Linux vet,
build, and smoke did not run after the test gate stopped the sequence. At
07:50:13 UTC all three Runner services were active, no test process remained,
and the synchronized checkout was clean at `9113b44`. Detailed output and
previous attempts remain in the [semantic test evidence](CAT-06-legacy-semantic-test-evidence-2026-10-22.md).

The exact command was `TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp
GOMAXPROCS=2 GOFLAGS=-p=1 make test`. The start samples showed CPU steal of
7%, 0%, and 18%. The observed package results were `cmd/catalog-probe-once`
(cached), `cmd/lane-preflight` (cached), `cmd/runner` (no test files), and
`cmd/runner-lane` (38.402 seconds). The suite then produced no further package
result before the 93% steal observation at 07:48:59 UTC. At 07:50:13 UTC,
`/tmp` had 15,610,396,672 bytes free, all Runner services were active, the
checkout remained clean, and no test process remained.

Mac, Sandbox, and Factory current-revision full gates passed; Mac's focused
CAT-06 tests also passed. Four mode-0600 compatibility inputs bound to the
reviewed evidence SHA-256
`99ccfc61ae6756895d253889135d8b11d6214f3787772f62524836bde0a10f33` passed
`catalog-compatibility-check` on all four hosts in `read_only_no_backup` mode.
Their per-host hashes and paths are in the
[compatibility record](CAT-06-legacy-compatibility-2026-10-22.md#compatibility-inputs-and-refreshed-retained-work--2026-10-22). No operational preflight or backup was run.

Sandbox had 906,756,096 bytes free at 07:56:08 UTC versus 2,496,689,137 bytes
required for the measured backup/staging estimate plus the separate 2 GiB
operating buffer, a 1,589,933,041-byte shortfall. The selector argument stays
67,108,864 bytes (64 MiB); the 2 GiB buffer is separate. Do not start a
backup or selector operation until the measured total fits.

Fresh read-only inventories still show seven retained lost pairs (Mac 3,
Linux 1, Sandbox 3, Factory 0), with no durable stop, cleanup, or release
proofs. Their current identities, markers, and process observations remain
linked above. Do not manually free their capacity; the supported recovery
installer also installs binaries and must be treated as an installation.
QAS and its eight mailbox paths remain in service. No retained work was
recovered/replayed, no PRD backup/selector/receipt/service/database changed,
and no mailbox request or ACK was published. No new candidate was sealed and
Dev→QAS acceptance is not complete; PRD remains blocked.

## CAT-06 checkpoint — 2026-10-22 07:45 UTC — public harness and three platform gates pass; PRD remains blocked

Source revision `9113b44fc436014c4017522ca2c2432ed66c79b9` is on Mac `dev`,
primary Gitea `origin/dev`, the exact automatic GitHub mirror `github/dev`,
and all three clean Ubuntu `dev` checkouts. The untracked
`src/cmd/catalog-holdcheck/` remains untouched. The public-command subprocess
harness and read-only legacy-compatibility validator are part of this
revision.

At this exact revision, Mac's focused CAT-06 tests passed; its full test, vet,
build, and smoke gates had passed in the preceding validation. Sandbox and
Factory each passed full `make test`, vet, build, and smoke, with restricted
concurrency and existing caches. Linux's corrected P025 and P101 targeted
tests now pass, but its full-suite gate remains incomplete: repeated fresh
`vmstat` samples reached 77–99% CPU steal with all three services active. One
current-revision full suite has not been started during those intervals. The
exact commands, package progress, prior interruptions, and current samples are
in the [semantic test evidence](CAT-06-legacy-semantic-test-evidence-2026-10-22.md).

Four canonical mode-0600 compatibility inputs are generated outside Git and
bound to the exact installed bundle/configuration hashes and semantic evidence
SHA-256 `ea976ed988edd0cbe49e82b6bb6e3e75cdfde515ecf1e34826bdb820c2ae21c7`.
Their current owner-only file hashes and read-only validation results are in
the [compatibility record](CAT-06-legacy-compatibility-2026-10-22.md#compatibility-inputs-and-refreshed-retained-work--2026-10-22). The
Mac, Sandbox, and Factory inputs passed `catalog-compatibility-check` in
`read_only_no_backup` mode. Linux's revised input is not yet validated. No
`catalog-host-preflight` was invoked; that command creates a backup.

Sandbox had 935,182,336 bytes free after its read-only compatibility check.
The current estimate for backup/staging plus the separate 2 GiB operating
buffer is 2,496,689,137 bytes, leaving a 1,561,506,801-byte shortfall. The
CLI's `--required-free-bytes` remains exactly 67,108,864 bytes for the selector
reserve; the 2 GiB buffer must not be passed to that flag.

A fresh read-only inventory again found three retained lost pairs on Mac, one
on Linux, three on Sandbox, and none on Factory. All seven still lack durable
stop, cleanup, and release proofs; Linux/Sandbox owner groups have only zombie
`bash` entries, and Mac's recorded groups were absent. Exact IDs, owner-marker
hashes, and state are linked in the compatibility record. No pair was
recovered, released, or replayed. No PRD backup, selector, receipt, service,
database, configuration, mailbox, or ACK changed. QAS and its eight mailbox
paths remain in service. CAT-06 deployment and release sealing remain blocked.

## Linux full-suite retry — 2026-10-22 06:02–06:15 UTC

The clean Linux `dev` checkout was at
`6ff1d0252358d2fb1d46d2a887b5bd4d7832631c`. PRD, Dev, and QAS `runnerd`
services were active before the run. With Go 1.27.1 and existing caches, the
bounded command used `GOMAXPROCS=2 GOFLAGS=-p=1 TMPDIR=/tmp GOTMPDIR=/tmp
SQLITE_TMPDIR=/tmp make test`. The initial `/tmp` backing filesystem had
15,322,692 KiB available.

`src/internal/dispatcher` passed in 421.356 seconds and
`src/cmd/runner-lane` passed in 0.058 seconds. The run then entered
`src/internal/execution`. `vmstat` at 06:10 UTC reported run queue 68 with 86%
CPU steal; its next sample was run queue 13 with 84% CPU steal. All three
Runner services remained active. To limit additional pressure on the starved
PRD host, SIGINT was sent to the exact task-owned `execution.test` process, then
to the exact task-owned `go test ./...` process. The execution package reported
`signal: interrupt`; `make test` exited 2. This run is **INTERRUPTED / NOT
PASS**, not a test assertion failure. The earlier separate Linux full-suite
timeout in `src/internal/localapi` remains a distinct non-passing result.

After all test processes exited, the task-owned `/tmp/go-build889206804` was
absent. The final read-only check at `2026-10-22T06:15:15Z` found all three
services active, `/tmp` backing storage with 15,319,676 KiB free, clean Git
state at the exact revision, and no test process. No Runner service, work,
source, database, selector, or receipt changed.

## Sandbox source gates — 2026-10-22 05:50–05:57 UTC

On `sandbox-host`, the clean `dev` checkout was synchronized at
`4305b6e7300b9a4f195ce35764589a91347dbe77`; its Runner source content is
unchanged from `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`. The explicit Go
toolchain was Go 1.27.1 (`linux/arm64`), and `TMPDIR`, `GOTMPDIR`, and
`SQLITE_TMPDIR` were `/tmp`, which is disk-backed on `/dev/sda1`. Existing Go
caches were reused.

The first `make test` call was issued from the home directory by mistake and
returned `make: No rule to make target 'test'`; it ran no tests. The corrected
call ran from `/home/ubuntu/projects/remote-session-runner` and passed the full
`go test ./...` suite with exit 0, ending at `2026-10-22T05:54:36Z`. Then
`make vet`, `make build`, and `make smoke` each passed at the same checkout
revision. Smoke built each command under its task-owned `/tmp` directory and
removed it after completion. The final read-only check at
`2026-10-22T05:57:03Z` found `/dev/sda1` available bytes `1,137,782,784` (about
1.06 GiB); no test process remained and the checkout was still clean.

The owner's agreement to continue under tight disk headroom applies to these
source gates only. Sandbox remains below the 2 GiB buffer required before
backups or selector operations. No cleanup, backup, release, receipt, service,
selector, database, mailbox, or ACK operation occurred. This closes the
Sandbox source-test, vet, build, and smoke gates; it does not pass the Linux
full-suite gate, the public `runner-lane` subprocess sequence, retained-work
recovery, typed legacy-compatibility inputs, Dev/QAS acceptance, or any PRD
deployment gate.

## Current capacity check — 2026-10-22 05:33 UTC

A fresh strict-host-key SSH observation on `sandbox-host` at `2026-10-22T05:33:50Z`
reported 1,215,143,936 bytes free on `/dev/sda1`, below the required 2 GiB
buffer by 932,339,712 bytes. `ulimit -f` was unlimited; `quota` is unavailable,
so no per-user quota was established. The prior 861 MiB reading is superseded.
No Sandbox mutation or test ran. See the [capacity and legacy compatibility
record](CAT-06-legacy-compatibility-2026-10-22.md#sandbox-capacity-recheck--2026-10-22-0533-utc).

## Retained-work refresh — 2026-10-22 05:37–05:42 UTC

The latest read-only PRD inventories still show seven retained lost pairs:
Mac 3, Linux 1, Sandbox 3, Factory 0. No queued/running/resumable work or
pending recovery-finalization rows were found. Mac recorded PIDs are absent;
Linux and Sandbox recorded process groups contain only their zombie `bash`
entries. Durable stop, cleanup and release timestamps remain empty, so the
pairs still require the supported exact-pair recovery path. Worker PRD services
remain active; the two Mac unsuffixed LaunchAgents are not loaded. No recovery,
service change, backup, selector mutation, mailbox request or ACK occurred.
Full IDs, inventory hashes, owner-marker results and command details are in
the [refreshed compatibility and work record](CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-retained-work-and-service-recheck--2026-10-22-05370542-utc).

## Current CAT-06 checkpoint — 2026-10-22 05:26 UTC — Factory gates pass; Linux full test gate fails; PRD remains blocked

The Linux and Factory checkouts were clean on `dev` at
`40a62d93d7eaa8bc567482aa3bfbab0dfb93befd` before validation. The Factory
full source gates now pass at this exact revision. The Linux full suite has a
new, separate timeout; its CAT-06 packages passed, and the non-test gates
passed:

| Host | Exact-revision checks | Result |
| --- | --- | --- |
| Factory | `TMPDIR=/var/tmp GOTMPDIR=/var/tmp SQLITE_TMPDIR=/var/tmp make test`, then `make vet`, `make build`, `make smoke` | **PASS** |
| Linux | `make test` (`go test ./...`) | **FAIL** — `src/internal/localapi` timed out after 10 minutes in unrelated `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, while fixture `store.Open` verified migration checksums. `src/internal/laneinstall` and `src/cmd/runner-lane` passed in that run. After recording the failure, only the remaining task-owned `go test ./...` process was interrupted to limit CPU load on the PRD host; `make test` exited 1. |
| Linux | `make vet`, `make build`, `make smoke` | **PASS** |

The 05:26 source gate remains incomplete because Linux `make test` did not
pass. The previous Linux full-suite timeout in the execution package remains
historical and distinct. No production service or retained Runner work was
stopped. Mac full gates passed at code commit `93e229d`; this evidence commit
changes documentation only. Sandbox's focused PRD/Catalog tests plus vet,
build, and smoke passed at the same source bytes; its full suite remains
unrun. The owner's latest Sandbox reading is 861 MiB free on `/dev/sda1`, below
the requested 2 GiB buffer. No additional Sandbox gates, backups, or selector
operations were attempted.

The current fixture evidence exercises PRD receipt construction, ordered
fences, the underlying reconciliation/rollback/synchronization APIs, and
launcher/wrapper restoration while preserving a database sentinel. A complete
`runner-lane` subprocess sequence for initial legacy adoption, successful
completion, interrupted switching, and reverse rollback on disposable
host-path fixtures has **NOT RUN**. Do not count the API-level fixtures as
that public-command acceptance.

The corrected seven-pair inventory and exact installed config/bundle hashes
remain in
[`CAT-06-legacy-compatibility-2026-10-22.md`](CAT-06-legacy-compatibility-2026-10-22.md).
The typed per-host compatibility inputs still need to be generated and
validated. All seven retained reservations remain without durable stop,
cleanup, or release timestamps. There is no supported recovery result yet.
No new release was sealed, no PRD backup/receipt/hold was created, and no
service, selector, database, mailbox, or ACK was changed. QAS remains serving
with the eight mailbox paths assigned to it.

## Current CAT-06 checkpoint — 2026-10-22 04:45 UTC — corrected compatibility evidence; Sandbox headroom and retained work still block

The owner supplied a fresh Sandbox reading of **861 MiB available** on
`/dev/sda1`. This remains below the requested 2 GiB operating buffer. Do not
start Sandbox source gates, PRD backups, or selector operations until a fresh
measurement meets the required headroom. No data, shared cache, mailbox,
backup, journal, service, selector, receipt, or database was changed in this
checkpoint.

The delivered source correction remains at `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`;
the preceding evidence update is at `40a62d93d7eaa8bc567482aa3bfbab0dfb93befd`.
Both were pushed to primary Gitea, the exact GitHub mirror SHA was verified,
and the three clean Ubuntu `dev` checkouts were fast-forwarded to the evidence
commit. The unrelated untracked `src/cmd/catalog-holdcheck/` directory remains
untouched. The rollback-semantic fixture passed on Mac, Linux, Sandbox, and
Factory at `40a62d9`; this was not a live rollback.

The compatibility evidence and corrected retained-work identities
are recorded in
[`CAT-06-legacy-compatibility-2026-10-22.md`](CAT-06-legacy-compatibility-2026-10-22.md).
That record binds the fresh per-host config and legacy-bundle hashes to schema
40, a read-only inspection with the installed source revision, and the
four-host launcher/wrapper/database-preservation fixture result. It is not a
live rollback, backup, or installed-service acceptance. The operational
`CatalogLegacyRollbackCompatibility` input has not yet been generated and
validated for each host; do not treat the Markdown record as that input.

The corrected inventory still contains seven unreleased terminal-lost
reservations. Their current command/session/job states are lost, but no durable
slot-stop, reservation-cleanup, or release timestamps exist. Mac process IDs
were absent and Linux/Sandbox recorded PIDs were zombies; those observations
are not the supported cleanup proof. The exact recovery path is not yet run.
Do not create the PRD receipt or claim an empty work inventory until the
supported recovery records the required disposition. QAS remains serving, all
eight mailbox paths remain assigned to QAS, and no mailbox request or ACK was
published.

No new release was sealed. Candidate qualification remains blocked on Sandbox
headroom, supported disposition of the seven retained pairs, the per-host
operational compatibility inputs and fresh backups. No Dev/QAS/PRD service,
selector, receipt, hold, database, or mailbox state changed.

## Current CAT-06 checkpoint — 2026-10-22 04:23 UTC — arm64 fixture correction delivered; PRD still blocked

The Sandbox run at the previous source exposed a **test-fixture platform
selection defect**: on Linux/arm64 the test selected the `linux-host`
`linux/amd64` plan, and the expected fixed-platform guard rejected it before
checking the isolated backup snapshot. The production code and installed PRD
were not changed. The regression now selects `sandbox-host` on Linux/arm64,
`linux-host` on Linux/amd64, and `mac-control-plane` on Darwin/arm64.

The correction is commit `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`. Mac
primary Gitea `origin/dev` and GitHub `github/dev` both resolve to this SHA.
The `linux-host`, `sandbox-host`, and `factory-sandbox` checkouts were clean on
the predecessor before an explicit-identity `pull --ff-only`; each is now clean
at this exact SHA. Mac's unrelated untracked `src/cmd/catalog-holdcheck/`
directory remains untouched and unstaged.

| Host | Validation at `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb` | Result |
| --- | --- | --- |
| Mac | `make test`, `make vet`, `make build`, `make smoke`, Go 1.27.1 `gofmt`, `git diff --check` | **PASS** |
| Linux | `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run '^(TestPRD|TestCatalog)' -count=1` | **PASS** |
| Sandbox | Same focused PRD/Catalog test command; `make vet`, `make build`, `make smoke` | **PASS** |
| Factory | Same focused PRD/Catalog test command | **PASS** |

Factory's full `make test`, vet, build, and smoke passed at predecessor
`a43a862`; Linux's vet, build, smoke, and focused tests passed there. Linux's
full `make test` at that predecessor remains **FAIL** at Go's 10-minute timeout
in unrelated `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice` during
fixture SQLite migration. The full Linux suite was not rerun at the test-only
correction. Sandbox full `make test` was not run; the CAT-06 focused suite and
all three non-test build gates above passed. These limits are not PASS claims.

Sandbox capacity observations: the owner's pasted `df -h` showed 861 MiB free.
Strict-host-key-verified SSH at `2026-10-22T04:14:06Z` measured 880,996 KiB
free on `/dev/sda1`; `/tmp`, `/var/tmp`, and `/home/ubuntu` share that ext4
filesystem, and `quota` is not installed. After the focused PRD/Catalog tests,
vet, build, and smoke, the latest reading at `2026-10-22T04:21:23Z` was
1,280,228 KiB free. The shared Go cache and all retained data were preserved.
This remains below the previously requested 2 GiB operating buffer. No Sandbox
backup, candidate, selector, receipt, hold, or service operation was attempted.

No new release was sealed. The exact legacy binary/configuration compatibility
report and tested data-semantic evidence remain outstanding; schema 40 alone
does not establish compatibility. The exact seven retained pairs and their
ownership markers from the prior inventory have not been refreshed in this
checkpoint, and supported recovery has not run. No production backups, receipts,
admission holds, selectors, services, databases, mailbox contents, or ACKs were
changed. QAS remains serving, all eight mailbox paths remain assigned to QAS,
and existing `cat-20261021-b84caf0` and historical `ebc4446` are unchanged.

## Current CAT-06 checkpoint — 2026-10-22 00:51 UTC — delivered source `a43a862`

The PRD bootstrap/reconciliation source correction and platform-aware fixture
fix are delivered on `dev` at `a43a862ba27e818f0ece0cc3a2a5a481723d40f7`.
Primary Gitea and the automatic GitHub mirror resolve to that exact SHA; all
three Ubuntu `dev` checkouts are clean at that SHA. The untracked
`src/cmd/catalog-holdcheck/` directory remains untouched and unstaged.
No CAT candidate was sealed, no selector or service was changed, and no PRD
backup, receipt, or admission hold was created.

Source validation at this exact source tree:

| Host | Required checks | Result |
| --- | --- | --- |
| Mac | `make test`, `make vet`, `make build`, `make smoke`, `gofmt`, `git diff --check` | **PASS** |
| Factory | full `make test`, `make vet`, `make build`, `make smoke` | **PASS**; temporary test files used disk-backed `/var/tmp` because this host mounts `/tmp` as tmpfs |
| Linux | focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run '^(TestPRD|TestCatalog)' -count=1`, `make vet`, `make build`, `make smoke` | **PASS** |
| Linux | full `make test` (`GOFLAGS=-p=1`) | **FAIL** — Go's 10-minute `src/internal/execution` timeout in unrelated `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`, while fixture `store.Open` applied migrations (`modernc.org/sqlite` B-tree insert). No assertion failure. Remaining task-owned test process was stopped after this failure; only its unique `/tmp/go-build91547467` was removed after process exit. |
| Sandbox | source tests and build gates | **NOT RUN** — fresh `/dev/sda1` availability was 443,699,200 bytes. The measured PRD backup requirement is 349,205,489 bytes, leaving only 94,493,711 bytes; do not risk filling this filesystem with Go tests. |

The owner previously pasted a Sandbox observation with 1.9 GiB available; a
fresh direct measurement on the same `oracle-gustaw-janecki-ubuntu-flex-02`
now reports 443,699,200 bytes free. No cache, retained record, backup, mailbox,
or journal data was removed. Recheck before any future operation.

### Fresh PRD baseline and capacity

The four installed native bundles report build revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`; that commit exists locally and is
an ancestor of the current source. Every listed service binary reported that
revision. All four databases report schema 40. The hashes identify installed
bytes only and do not establish compatibility by themselves.

| Host | PRD service | Config SHA-256 | Legacy bundle SHA-256 | Available / required bytes | Canonical work counts `(sessions, slots, reservations, queued, running, resumable, finalizations)` | Inventory SHA-256 |
| --- | --- | --- | --- | ---: | --- | --- |
| Mac | stopped | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 147,109,834,752 / 392,873,782 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | active | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 15,768,182,784 / 126,470,672 | `(0,1,1,0,0,0,0)` | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | active | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 443,699,200 / 349,205,489 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | active | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 34,951,086,080 / 108,012,935 | `(0,0,0,0,0,0,0)` | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

Fresh PRD preflight used the Go 1.27.1 source command and its read-only SQLite
opener on Mac, Linux, and Factory. Sandbox hashes were freshly recomputed from
the config and exact binary files without writing to its nearly full disk;
its exact work rows were read in a compatible SQLite read transaction. The
host's system `sqlite3` library rejected the Linux schema's `STRICT` table
syntax, so Linux work details were instead read using the repository's Go
1.27.1 `modernc.org/sqlite` driver, `mode=ro`, `query_only=ON`, and one
consistent transaction. No normal `store.Open`, migration, checkpoint, or
database write was used.

### Refreshed retained-work pairs

**Correction:** the session-to-command/job associations in the table below
were transcribed incorrectly. Keep this historical snapshot for audit, but do
not use it for recovery. The corrected four-host inventory, owner identities,
marker digests, and process observations are in the linked
[`2026-10-22 compatibility evidence`](CAT-06-legacy-compatibility-2026-10-22.md#fresh-retained-work-and-ownership-observations).

All seven commands, sessions, and jobs remain terminal `lost`; command output
is incomplete, teardown is `lost`, and every command-slot stop/release and
session-reservation cleanup/release timestamp is unset. There are no queued or
running commands, resumable jobs, or pending recovery finalizations. The
owner-only runtime markers remain mode 0600 with one link and no recorded
lost-recovery cleanup confirmation. These are retained ownership records, not
authorization to release capacity.

| Host | Session → command → job | Runtime generation; owner PID/process observation |
| --- | --- | --- |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-a66ce02cbaddf294e13afb3a409c317c` → `job-c40088894069e76f12650ea3bb0376bc` | `c1e43518ad81e68495c65e69b996ec02`; PID 54385 absent; marker SHA-256 `d47128d18d241ab9c0364a08df609c4f89657a33cd5fe5b6c4c189926bfb4631` |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` → `job-2b4d2e3c9682d54e89342f7a486b5ddd` | `91e709684a1f4b02c35ab53c9d73b30c`; PID 12701 absent; marker SHA-256 `582aa7f474104a738a03368664630d2d453855e896f944b8d9d419a7693e9c91` |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-b524b94074ee26855f1778d52c9655cf` → `job-d46393c75fc5a21dc2fa38a58329d2bc` | `337ab8c2bd8e79f6f369ecf3e934cf2a`; PID 22367 absent; marker SHA-256 `02862e07e173fa4d75071b821179bd3c48c552904d593d83af4a9ca4972ced93` |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` → `job-a73371770d4d29e2fc22fafd135396e9` | `linux-6783b82a3e9f846e79ca28e4c3085e96`; PID/PGID 118705 is a zombie whose start identity matches the marker; marker SHA-256 `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e` |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` → `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe` | `linux-c63e12bbd7246fd7c41773c188c8a958`; PID/PGID 2851086 zombie; marker SHA-256 `c932f38efb1695dfdc8f34e2e4663ad7ffcb2f74ea857a8443dbb4b86597ef49` |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-8cf6db5bb3871e721133825520367598` → `job-77ea4fda0582a7e9d3e49d269830d6ba` | `linux-9e7b4ccbe132f0fce46d51f1636fe92f`; PID/PGID 3969302 zombie; marker SHA-256 `e060c9e338cb8d59da47bcae7dd1ec106098da6c2e89598072dbdb84fa4c8177` |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-a06764547029ef15e4726ebd56a03cdf` → `job-3cb15f466008a37dc2bb719d83a573d4` | `linux-aefd261855c13ca0b8461d575cefb174`; PID/PGID 3253996 zombie; marker SHA-256 `a49c560f83ebe81c1bf65e7b156f051f268dc60982bc06398c1a5955b7f8d0d5` |
| Factory | none | No unreleased work rows or ownership markers |

Mac's absent process IDs and Linux/Sandbox zombie states are observations, not
the persisted supported stop proof: the DB timestamps remain unset. The
supported recovery must revalidate exact process-group ownership and write
its own cleanup/stop evidence. No result, history, workspace, marker, or ACK
was changed.

### Deployment gates still open

The legacy compatibility report has **not** been generated or accepted. It
must bind each exact old bundle/configuration hash above to a private,
consistent database backup and tested data semantics; schema 40 is not that
evidence. Sandbox needs more disk-backed headroom before its source suite and
any backup. The sealed candidate and final per-host hashes do not exist yet.
Dev qualification and then QAS qualification remain required; QAS users are
active, so the owner must coordinate their pause and drain before any QAS
restart. Fresh selected-lane guards, supported retained-work recovery,
consistent backups, synchronized PRD receipt/hold, cohort switching and
postflight remain unrun. Existing b84caf0 and historical ebc4446 releases are
unchanged; all eight mailbox paths remain assigned to QAS. No mailbox request
or ACK was published.

## Current engineering update — 2026-10-21 23:03 UTC: bootstrap and rollback-source work implemented; no candidate sealed

Source work based on Mac `dev` predecessor `b5683e69507a58a93827c57500da4a315e1b6c3d` now adds a supported initial PRD cohort path. `catalog-host-preflight --lane prd` captures a read-only PRD snapshot, writes a consistent online backup, then verifies that the schema and canonical seven-field work inventory in the backup match the live preflight. The coordinator can create a PRD receipt without a pre-existing PRD hold; four ordered `fence-host` transitions stop and record each selected unsuffixed service, and only then may the receipt enter `held`. The public `preflight` action requires that exact synchronized hold.

The catalog and PRD recovery paths now hash one canonical work-inventory representation covering active sessions, command slots, reservations, queued and running commands, resumable jobs, and pending recovery finalizations. PRD local receipt validation distinguishes an unselected legacy selector, selected catalog state, partial switching, and recorded rollback. Rollback reconciliation checks the matching selector journal, recorded rollback digest, restored previous selector, preserved configuration/schema, and restored legacy launcher/wrapper bytes from the consistent backup; it does not restore an old database.

Fixture coverage exercises PRD receipt creation and ordered fences, selected/partial-switch reconciliation, reverse rollback with synchronization after each host, rollback completion synchronized to all four fixture hosts, and restoration of legacy launcher/queued-wrapper bytes while preserving a database sentinel. The canonical inventory/backup match and mismatched identity/journal rejection also have regressions. These are source and disposable-fixture results; they do not establish a live PRD backup, exact legacy-binary compatibility, retained-work recovery, service deployment, or functional acceptance.

Mac source gates on this working tree: focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1` **PASS**; `make test` first failed in unrelated `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` because sandboxed `/bin/ps` returned `operation not permitted`, then the full rerun with process inspection **PASS**; `make vet`, `make build`, and `make smoke` **PASS**. `git diff --check` **PASS**. The exact source commit is pending the required Git delivery. No source or service changes were made on Ubuntu, and no PRD/Dev/QAS service, selector, database, backup, receipt, mailbox, or ACK was changed in this turn.

Validation follow-up — 2026-10-21 23:39 UTC: delivery of `140b0e94cd1be817695c77219208bea455133401` exposed a cross-platform fixture bug. Its `TestPRDWorkSnapshotUsesCanonicalFullInventoryAndVerifiesBackup` selected the Mac PRD plan even on Linux; the production platform guard correctly rejected it. The Mac-only focused test passed. The source correction now selects `mac-control-plane` on Darwin and `linux-host` on Linux; that change is not yet delivered or validated on Ubuntu.

The synchronized `make test` run on Factory exited 2 on that one fixture failure. On `linux-host`, the full suite hit Go's 10-minute package timeout first in `src/internal/execution` at `TestP026I04TeardownFailurePreservesCommandOutcomeAndStableRetry`, inside SQLite `fsync` during test fixture `store.Open`; later package timeouts occurred in `src/internal/laneinstall` while creating a fixture schema and `src/internal/localapi` while opening another fixture. The live `ps` snapshots showed these processes under the task-owned `/tmp/cat06.emvA2N/go-build...` tree. The run was terminated after repeated timeouts; its process group exited and the task-owned temporary tree was confirmed removed. The host still had 15,446,192 KiB available on `/dev/sda1` afterward. The evidence does not establish why these unrelated full-suite SQLite fixtures were this slow. The full Linux and Factory test gates are **NOT PASS**; no vet/build/smoke run followed those `set -e` test failures. Sandbox host gates were not started because `/dev/sda1` had only 532,760 KiB available and its existing 4.3 GiB Go cache was preserved. Focused CAT-06 tests and remaining host gates still need validation against a delivered correction.

No new release was sealed: the existing accepted `cat-20261021-b84caf0` and historical `ebc4446` artifacts remain untouched. All eight mailbox paths remain assigned to QAS under the active handoff; QAS was not restarted or reconfigured. Mailbox request/ACK publication and mailbox acceptance remain **NOT RUN**.

Validation update — 2026-10-21 23:43 UTC: the platform-aware fixture correction is present in the Mac working tree but not yet delivered. On that exact working tree, full `make test`, `make vet`, `make build`, `make smoke`, `git diff --check`, and the Go 1.27.1 `gofmt` check all **PASS** on Mac. The test command compiled the pre-existing untracked `src/cmd/catalog-holdcheck/` directory; it remains untouched and unstaged.

Fresh host preflight on 2026-10-21: Linux host `/tmp` is on `/dev/sda1` with 15 GiB available; Factory `/tmp` is a 4 GiB tmpfs (3.4 GiB available), while its disk-backed `/var/tmp` is on `/dev/sda1` with 33 GiB available. Do not put task temporary files on Factory tmpfs; use its disk-backed temporary area for source checks. Sandbox `/tmp` is on `/dev/sda1` and has only 489 MiB available, so do not start its tests or backups yet. This fresh measurement supersedes the earlier 532,760 KiB reading and differs from the owner's pasted 1.5 GiB snapshot. No shared cache or host data was removed.

Before a candidate can be sealed or PRD can change, complete Git delivery and the host source gates, generate and verify host-specific legacy rollback compatibility evidence bound to each exact PRD binary-set/configuration hash and tested data semantics, refresh exact retained-work identities and ownership/stop evidence, and prepare fresh consistent backups/capacity. The owner reports QAS users are active; coordinate their pause and drain before any QAS restart required for qualification. No external publisher pause was inferred from mailbox or directory counts.

## Current status — 2026-10-21 22:01 UTC: PRD migration blocked; mailbox handoff active

CAT-06 PRD catalog migration remains **BLOCKED** on its existing bootstrap,
retained-work and compatibility gates. The separate owner-approved mailbox
handoff is active: eight existing paths are assigned to QAS, while Mac PRD is
stopped with an empty mailbox map and disabled ingress. Its original installed
binaries, database and keys remain preserved; workers' PRD services are
unchanged. This routing change does not complete PRD adoption or promotion.

Dev and QAS now accept the same immutable `cat-20261021-b84caf0` bundles, with
released four-host operations and passing scoped/ordinary API routes. See
[the handoff record](MAILBOX-HANDOFF-2026-10-21.md) for current identities,
focused validation scope and preservation evidence. Clients remain paused.
No filesystem mailbox request or ACK was published; filesystem-mailbox
acceptance remains **NOT RUN**.

## Historical status — 2026-10-21 18:56 UTC: PRD blocked; Dev/QAS independently opened

Dev and QAS now permit normal API use on the unchanged accepted
`cat-20261021-ebc4446` release. Both four-host operations are `released`; all
seven ordinary routes per lane passed, and the final postflight found all
eight host/lane services ready with empty work inventories and zero error
counters. See [Dev/QAS admission opening](CAT-Dev-QAS-admission-open-2026-10-21.md).
PRD process identities, configuration and binary hashes are unchanged. The
CAT-06 bootstrap, retained-work and legacy-compatibility blockers below remain
unresolved; opening Dev/QAS does not satisfy those PRD gates.

No external publisher control was operated; the existing publisher pause
instruction remains in force. No mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**.

## Historical PRD preflight — 2026-10-21 17:44 UTC

**Status — 2026-10-21 17:44 UTC: BLOCKED before PRD mutation.** The frozen
release `cat-20261021-ebc4446` remains accepted in Dev and QAS. No PRD service,
selector, database, backup, receipt, journal, mailbox, or ACK was changed in
this check. Filesystem-mailbox acceptance remains **NOT RUN**.

## Read-only production baseline

The current PRD preflight used the synchronized source checkout at
`363ef81c4f03dcad3cd5d9c0483845f590e36dc8`; its tracked `src/` bytes match the
accepted ebc4446 source. Mac used the Makefile's Go 1.27.1 toolchain and its
shared Go caches. Worker preflights used the explicit Go 1.27.1 toolchain in
their clean `dev` checkouts and strict SSH host-key verification. The commands
were `runner-lane prd-preflight --host HOST` on each host. This command calls
`ReadPRDPreflight`, which uses the read-only restart-preflight SQLite opener;
it does not use `store.Open`, run migrations, or write the database.

| Host | Service active | Schema | PRD config SHA-256 | Legacy binary-set SHA-256 | Free bytes / preflight estimate | Retained command slots / reservations |
| --- | --- | ---: | --- | --- | ---: | ---: |
| `mac-control-plane` | yes | 40 | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 146,793,422,848 / 396,106,654 | 3 / 3 |
| `linux-host` | yes | 40 | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 16,201,306,112 / 126,462,432 | 1 / 1 |
| `sandbox-host` | yes | 40 | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 1,401,548,800 / 347,526,129 | 3 / 3 |
| `factory-sandbox` | yes | 40 | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 35,415,683,072 / 108,004,695 | 0 / 0 |

The current exact PRD binary hashes are also present in the emitted preflight
JSON. No installed source revision was inferred from them. All four services
remain on their unsuffixed legacy installation; PRD selectors remain
unselected. Dev and QAS were not changed.

The owner-provided Sandbox transcript shows `sudo rm -rf *` in
`/var/log/journal` and about 1.5 GB free afterward. Runner did not run that
command or modify journal files. A later read-only SSH measurement at
`2026-10-21T17:37:43Z` reported `/dev/sda1` and `/tmp` with 1,422,868,480 bytes
available (98% used). The lower preflight measurement above is the conservative
capacity input. The estimated PRD backup fits at that observation, but no
backup was created. Re-measure the actual requirement immediately before any
backup, stage, or selector operation.

The preflight counters show no active sessions, queued commands, running
commands, resumable jobs, or pending recovery finalizations. They do show
unreleased terminal work reservations: Mac 3, Linux 1, Sandbox 3, Factory 0.
The last exact read-only pair inventory classified all seven retained pairs as
`lost` in command, job, session, and teardown state; their stop/release and
cleanup/release timestamps were unset. The current preflight reconfirmed the
counts but did not re-enumerate pair identities, so exact identities and
process ownership must be refreshed immediately before supported recovery.
No scripts or payloads were read, replayed, deleted, or acknowledged.

The current turn did not independently verify the external publisher controls.
The owner’s pause instruction remains in force; the exact publisher-control
evidence must be captured before PRD admissions are fenced or services are
stopped.

## CAT-06 execution blocker

The accepted ebc4446 operator has no supported way to create the initial PRD
cohort receipt and its host preflights:

- `runner-lane catalog-cohort --action create` rejects PRD at
  `src/cmd/runner-lane/main.go:336-340`.
- `NewCatalogCohortReceipt` accepts only Dev/QAS at
  `src/internal/laneinstall/catalog_cohort.go:171-177`, despite the receipt
  validator supporting PRD receipts.
- `catalog-host-preflight` rejects PRD at
  `src/cmd/runner-lane/main.go:867-870`, and cohort `preflight` rejects PRD
  at `src/cmd/runner-lane/main.go:489-492`.
- `BeginCatalogPRDMaintenance` requires that the same PRD operation already
  be recorded as the unique local admission hold
  (`src/internal/laneinstall/catalog_prd.go:73-82`). This leaves no supported
  command path to produce and synchronize the first PRD hold after consistent
  backup and quiescence checks. The host-local `catalog-prd-backup` command
  does not create or synchronize the four-host cohort receipt.
- For an unselected legacy PRD selector, `readPRDCatalogProof` deliberately
  sets `readSchemaMax = 0` until tested cohort evidence exists
  (`src/internal/laneinstall/catalog_prd.go:440-445`). The old binaries have no
  catalog manifest declaring their read compatibility. A source change must
  bind the exact legacy bundle identity to actual compatibility evidence; it
  must not fill this field from the database schema number alone.

The seven terminal-lost reservations are a separate work guard. The existing
supported recovery procedure can dispose only freshly revalidated exact pairs
after selected services are stopped. No stop or recovery was attempted here.

## Required correction and sequence

Before selecting PRD, add and test a supported PRD host-preflight and
coordinator receipt path. It must preserve the current PRD restriction on
ordinary installers; require explicit publisher-pause evidence; use fresh
service/work/ownership facts; bind exact legacy binary and configuration
identities; create verified consistent backups; and record tested
previous-binary/current-data compatibility. Receipt creation and synchronization
must remain supported APIs; no manual receipt editing is acceptable.

Because that is a source change, it requires source gates and Git delivery,
then a **new sealed release identity** and renewed full acceptance in Dev and
QAS before PRD. The ebc4446 candidate remains unchanged and must not be
re-labeled to include the correction. After those gates, refresh exact retained
work identities and publisher controls, use only supported recovery, recheck
Sandbox capacity, create and synchronize the PRD hold, then follow the fixed
worker order and final Mac switch. The prior installation/restart authorization
remains scoped to this approved catalog migration, conditional on those gates.

At this evidence point no PRD backup, hold, service stop/restart, release
publication, selector change, functional probe, request, or ACK was performed.
