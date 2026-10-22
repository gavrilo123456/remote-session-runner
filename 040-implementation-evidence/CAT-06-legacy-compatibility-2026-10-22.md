# CAT-06 legacy PRD compatibility evidence — 2026-10-22

## Current capacity-policy override — 2026-10-22 08:08 UTC

The owner reduced the additional CAT-06 operating buffer to **0.5 GB =
500,000,000 bytes**. Older 2 GiB requirements and resulting deficits in the
dated observations below are superseded, not current blockers by themselves.
Preserve the existing backup estimator and separate 64 MiB selector reserve.
For the recorded 349,205,489-byte allowance, the revised required availability
is **849,205,489 bytes**. Always remeasure immediately before live operations.
The installed-identity/semantic compatibility records are unchanged; reducing
the buffer neither disposes retained work nor proves deployment acceptance.
See [policy and measured breakdown](CAT-06.md#owner-capacity-policy-amendment--2026-10-22-0808-utc).

## Scope

This record binds the existing PRD binaries and application configuration on
the four installed hosts to read-only checks of their current schema-40 data
and rollback-semantic fixture tests. It is evidence for legacy read and
artifact-restoration compatibility; it is **not** live rollback or deployment
acceptance. The checks did not stop or restart services, change selectors,
write databases, execute retained scripts, or publish mailbox requests/ACKs.

## Exact installed identities and data snapshots

All installed Runner components report source revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`. The service binary sets and
configuration files were freshly hashed by the read-only PRD preflight. The
four databases report schema 40.

| Host | PRD service | Config SHA-256 | Legacy bundle SHA-256 | Schema | Canonical work inventory `(sessions, slots, reservations, queued, running, resumable, finalizations)` | Inventory SHA-256 |
| --- | --- | --- | --- | ---: | --- | --- |
| Mac | inactive | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 40 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | active | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 40 | `(0,1,1,0,0,0,0)` | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | active | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 40 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | active | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 40 | `(0,0,0,0,0,0,0)` | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

Each installed native service component's `--version` reported the same
revision above. Mac launchd `ProgramArguments` resolve to the preserved PRD
`bin/runner-local` and `bin/runner-locald` with the existing PRD config. On all
three Ubuntu workers, `runnerd.service` starts the preserved
`bin/runnerd-entrypoint.sh` (SHA-256
`83e5e56d300eeea4a56a336c13706ed7de7697f324e22278e9e3fbdf6be9f2f3`), which
executes the preserved `bin/runnerd`. Their queued forced-command wrapper
`bin/runner-ssh-bridge-forced.sh` (SHA-256
`85947f4d76b08c415ab7c98b4e698f9317bdb0dc24c02b041fece6a0ad5d0809`)
executes the preserved `bin/runner-ssh-bridge` and uses the existing
authenticated-key/controller-map/socket arguments. No service definitions or
wrappers were changed.

## Read-only check using the installed source revision

The `runner-lane prd-preflight --host HOST` diagnostic was built from exact
source revision `00c1ca6e7694842c35d398583b3f26fea59e3613` with Go 1.27.1 for
each host architecture, then run locally on Mac and from `/tmp` on the three
workers. The diagnostic binaries were removed after their processes exited.
At that revision, `ReadPRDPreflight` calls
`store.OpenExistingRestartPreflightReadOnly`; it does not call `store.Open`,
create sidecars, or apply migrations. All four invocations exited 0 and read
schema 40, the same config and component hashes, and the same work counts as
the synchronized current-source preflight. This demonstrates that the
installed source revision's read-only database path can inspect the actual
current data without migration.

## Fresh retained-work and ownership observations

A read-only modernc SQLite transaction at `2026-10-22T04:33Z` enumerated only
unreleased command slots/reservations. It read identities and state metadata,
not scripts, output payloads, credentials, or raw SQL values. Every marker was
owner-only, singly linked, and matched its session ID and runtime generation.
All seven command/session/job tuples are `lost`; the command terminal event is
`command_lost`; command output is incomplete; job teardown is `lost`; and slot
stop/release plus reservation cleanup/release timestamps are unset. Markers do
not contain a lost-recovery cleanup timestamp. These are not durable stop
proofs.

| Host | Session → command → job | Generation; owner (UID); recorded PID/PGID; start identity | Marker SHA-256 | Process observation |
| --- | --- | --- | --- | --- |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` → `job-2b4d2e3c9682d54e89342f7a486b5ddd` | `91e709684a1f4b02c35ab53c9d73b30c`; `tomasz.walczuk` (502); 12701/12701; `1792524938329971` | `582aa7f474104a738a03368664630d2d453855e896f944b8d9d419a7693e9c91` | Recorded PID/PGID absent from direct `ps` |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-a66ce02cbaddf294e13afb3a409c317c` → `job-c40088894069e76f12650ea3bb0376bc` | `337ab8c2bd8e79f6f369ecf3e934cf2a`; `tomasz.walczuk` (502); 22367/22367; `1792525566416842` | `02862e07e173fa4d75071b821179bd3c48c552904d593d83af4a9ca4972ced93` | Recorded PID/PGID absent from direct `ps` |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-b524b94074ee26855f1778d52c9655cf` → `job-d46393c75fc5a21dc2fa38a58329d2bc` | `c1e43518ad81e68495c65e69b996ec02`; `tomasz.walczuk` (502); 54385/54385; `1792510335431810` | `d47128d18d241ab9c0364a08df609c4f89657a33cd5fe5b6c4c189926bfb4631` | Recorded PID/PGID absent from direct `ps` |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` → `job-a73371770d4d29e2fc22fafd135396e9` | `linux-6783b82a3e9f846e79ca28e4c3085e96`; `ubuntu` (1001); 118705/118705; `38701379` | `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e` | PID/PGID 118705 observed zombie (`Z`, `bash`) |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` → `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe` | `linux-c63e12bbd7246fd7c41773c188c8a958`; `ubuntu` (1001); 2851086/2851086; `625457721` | `c932f38efb1695dfdc8f34e2e4663ad7ffcb2f74ea857a8443dbb4b86597ef49` | PID/PGID 2851086 observed zombie (`Z`, `bash`) |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-a06764547029ef15e4726ebd56a03cdf` → `job-3cb15f466008a37dc2bb719d83a573d4` | `linux-9e7b4ccbe132f0fce46d51f1636fe92f`; `ubuntu` (1001); 3969302/3969302; `645723329` | `e060c9e338cb8d59da47bcae7dd1ec106098da6c2e89598072dbdb84fa4c8177` | PID/PGID 3969302 observed zombie (`Z`, `bash`) |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-8cf6db5bb3871e721133825520367598` → `job-77ea4fda0582a7e9d3e49d269830d6ba` | `linux-aefd261855c13ca0b8461d575cefb174`; `ubuntu` (1001); 3253996/3253996; `642533456` | `a49c560f83ebe81c1bf65e7b156f051f268dc60982bc063c1a5955b7f8d0d5` | PID/PGID 3253996 observed zombie (`Z`, `bash`) |
| Factory | none | — | — | No unreleased reservation or slot rows |

The Mac process check was run through the permitted direct shell after the
unprivileged sandbox rejected `ps`; the elevated check was read-only and
signalled no process. Linux and Sandbox zombies are observations, not persisted
cleanup confirmation. No retained work was released or replayed.

## Rollback-semantic fixture regression

At delivered source revision `40a62d93d7eaa8bc567482aa3bfbab0dfb93befd`,
`go test ./src/internal/laneinstall -run
'^TestPRDLegacyRollbackRestoresLauncherAndQueuedWrapperWithoutChangingDatabase$'
-count=1 -v` passed on Mac, Linux, Sandbox, and Factory. The fixture restores
legacy entrypoint and queued-wrapper bytes from a backup manifest, verifies
their modes and contents, and proves a database sentinel's bytes are
unchanged. It does not start a worker or use production scripts. This proves
the rollback code's artifact-restoration/data-preservation semantics in an
isolated fixture; it is not a live production rollback.

## Gate status

The exact installed binary/configuration identities and live read-only data
semantics are now recorded, and the required rollback fixture passes on all
four host platforms. No PRD backup was created in this evidence-only step; no
service, selector, receipt, hold, database, configuration, mailbox, or ACK was
changed. Fresh consistent backups, final work disposition/recovery, a new
sealed release, Dev then QAS acceptance, and the guarded PRD switch remain
separate gates. Do not treat this report as authorization to skip any of them.

## Sandbox capacity recheck — 2026-10-22 05:33 UTC

From Mac, a read-only SSH command to `sandbox-host` using the configured
`dev.slidestud.io` identity and strict host-key checking observed at
`2026-10-22T05:33:50Z`:

```text
/dev/sda1  size=54958841856 used=53726920704 avail=1215143936 use=98%
ulimit -f: unlimited
quota: command unavailable
```

The observed free bytes are 932,339,712 below the 2 GiB preflight buffer. The
previous 861 MiB report is stale. The shell file-size limit does not measure a
filesystem quota, and this host has no `quota` command available for a
per-user quota report. No writes or cleanup were performed; no Go/shared cache,
backup, retained work, mailbox, service, or selector was touched. Do not start
Sandbox tests, backup, or catalog mutation until a fresh disk-backed capacity
check meets the 2 GiB buffer.

## Fresh PRD retained-work and service recheck — 2026-10-22 05:37–05:42 UTC

Read-only database snapshots on all four PRD authorities confirmed schema 40
and the same seven unreleased command-slot/session-reservation pairs. The
canonical work counts and hashes are unchanged:

| Host | Active sessions | Unreleased slots | Unreleased reservations | Queued | Running | Resumable | Finalizations | Inventory SHA-256 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Mac | 0 | 3 | 3 | 0 | 0 | 0 | 0 | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | 0 | 1 | 1 | 0 | 0 | 0 | 0 | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | 0 | 3 | 3 | 0 | 0 | 0 | 0 | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | 0 | 0 | 0 | 0 | 0 | 0 | 0 | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

The seven exact session/command/job tuples remain those in the table above;
each still has state `lost` / `lost` / `lost`, incomplete command output, and
empty slot-stop, slot-release, reservation-cleanup, and reservation-release
timestamps. Current ownership observations were:

| Host | Pair(s) | Owner record | Process-group observation |
| --- | --- | --- | --- |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` / `cmd-9e2033c5d6106b2e88f2cff200d7dbfe`; `sess-a68baa8f1dd9e66c2871e128002240ef` / `cmd-a66ce02cbaddf294e13afb3a409c317c`; `sess-cf91a7902aceebd6ef20daaf6f090514` / `cmd-b524b94074ee26855f1778d52c9655cf` | All three existing owner markers remain mode `0600`, UID 502, link count 1, matching the session and generation; hashes match the previously recorded markers. PIDs 12701, 22367, and 54385 are absent. | No recorded PID is running. `launchctl print gui/502/com.remote-session-runner.local` and `.locald` both report no such loaded service. |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` / `cmd-6618114b0f1100e4d50479cf0e57ea2c` / `job-a73371770d4d29e2fc22fafd135396e9` | Marker SHA-256 `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e`; mode `0600`, UID 1001, link count 1; session/generation, PID/PGID 118705, owner, and start identity match the database. | The recorded PGID contains only PID 118705, UID 1001, zombie `bash`; no other process-group members were observed. `runnerd.service` is active (PID 3858597). |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` / `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` / `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe`; `sess-5abb5d808cd5623f3dbd49202f15822c` / `cmd-a06764547029ef15e4726ebd56a03cdf` / `job-3cb15f466008a37dc2bb719d83a573d4`; `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` / `cmd-8cf6db5bb3871e721133825520367598` / `job-77ea4fda0582a7e9d3e49d269830d6ba` | All three existing owner markers remain mode `0600`, UID 1001, link count 1, matching session/generation and recorded PID/PGID; marker hashes match the previously recorded values. | Each recorded PGID (2851086, 3969302, 3253996) contains only its zombie `bash` PID; no other process-group members were observed. `runnerd.service` is active (PID 2353257). |
| Factory | none | none | `runnerd.service` is active (PID 1970144). |

The SQLite inventory reads used a read-only transaction. Linux's installed
SQLite 3.31 CLI does not understand the newer `STRICT` schema syntax; the
bounded inventory query used its `-readonly` mode, `PRAGMA query_only=ON`,
and a connection-local `PRAGMA writable_schema=ON` to skip parsing unrelated
newer table declarations. No database file or schema was changed. The installed
revision's schema-40 read-only preflight had previously passed independently.

These observations refresh identity and process-group state; they do not create
the durable stop or cleanup proofs that supported retained-capacity recovery
requires. No recovery command, process signal, database write, service action,
backup, selector, mailbox publication, or ACK occurred. The exact seven pairs
remain blocked on supported recovery.

## Sandbox source-gate result — 2026-10-22 05:50–05:57 UTC

The synchronized Sandbox `dev` checkout was clean at evidence revision
`4305b6e7300b9a4f195ce35764589a91347dbe77`, with Runner source unchanged from
`93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`. Go 1.27.1 (`linux/arm64`) was
invoked explicitly. `TMPDIR`, `GOTMPDIR`, and `SQLITE_TMPDIR` used disk-backed
`/tmp`; the existing account caches were reused. Before the corrected test
run, `/tmp` had 1,212,305,408 bytes available. The full `make test` ended
successfully at `2026-10-22T05:54:36Z` with 1,140,256,768 bytes available.
`make vet`, `make build`, and `make smoke` then passed. A final read-only check
at `2026-10-22T05:57:03Z` found 1,137,782,784 bytes available, a clean checkout,
and no remaining test/build process.

The first test invocation was made from the home directory and returned
`make: No rule to make target 'test'`; no tests ran in that attempt. The
corrected invocation ran from the synchronized repository and passed the full
suite. This attempt and correction are both retained in evidence. The owner's
agreement to proceed with source gates under tight capacity did not waive the
2 GiB minimum for backups or catalog selector operations. No cleanup, backup,
service, selector, receipt, database, mailbox, or ACK changed in this gate.

## Compatibility inputs and refreshed retained work — 2026-10-22

The reviewed semantic evidence is
[`CAT-06-legacy-semantic-test-evidence-2026-10-22.md`](CAT-06-legacy-semantic-test-evidence-2026-10-22.md), SHA-256
`99ccfc61ae6756895d253889135d8b11d6214f3787772f62524836bde0a10f33`.
The four canonical JSON inputs use format version 1, schema/read range 40,
test suite `catalog-prd-legacy-semantics-v1`, the five required semantic check
IDs, and tested time `2026-10-22T07:50:13Z`. Each input is bound to the exact
legacy bundle and configuration digests in the first table above.

| Host | Owner-only input path | SHA-256 | File identity |
| --- | --- | --- | --- |
| Mac | `/private/tmp/cat06-legacy-compatibility-mac-control-plane-20261022-r4.json` | `acc1a1d445519cda69b4fca9def7d27b3defdfeaa273953b299c814fde5e7412` | mode 0600, UID 502, one link |
| Linux | `/home/ubuntu/.local/share/remote-session-runner/cat06-legacy-compatibility-linux-host-20261022-r4.json` | `4a75b3b4c3d365fcb51a3ea10c52cfe618d2e61b00251807786af52f1dcfe224` | mode 0600, UID 1001, one link |
| Sandbox | `/home/ubuntu/.local/share/remote-session-runner/cat06-legacy-compatibility-sandbox-host-20261022-r4.json` | `647762789eb2af19e4f21cf4e2fce1d214015ed5df18fdfac100325d4211ace8` | mode 0600, UID 1001, one link |
| Factory | `/home/ubuntu/.local/share/remote-session-runner/cat06-legacy-compatibility-factory-sandbox-20261022-r4.json` | `844bf6583cdc27876be0065eedb56692e806ba71aa1ec0b184fdea5bbc16c693` | mode 0600, UID 1001, one link |

At 2026-10-22 07:55–07:56 UTC, the exact-revision
`runner-lane catalog-compatibility-check` returned `read_only_no_backup` on
all four hosts. It reported each exact bundle/config hash, schema 40, and the
same work-inventory hash as the fresh PRD table; service state was false on
Mac and true on all three workers. Older task-owned input revisions r1–r3
were removed only after their hashes and ownership were verified; only the
current r4 files remain. `catalog-host-preflight` creates a consistent backup
and is not a dry run. No backup was created by these read-only checks.

The Sandbox filesystem calculation remains a separate live-operation gate.
The required pre-backup amount is 349,205,489 bytes (consistent backup/staging
estimate plus the 67,108,864-byte selector reserve); the 2 GiB operating buffer
is additional, for a total of 2,496,689,137 bytes. At 07:56:08 UTC, Sandbox
had 906,756,096 bytes free on `/dev/sda1`, a 1,589,933,041-byte shortfall. The
shared cache was not cleaned. No backup, journal, database, mailbox, service,
receipt, selector, or retained work was removed or changed.

Fresh read-only SQLite snapshots at approximately 07:39 UTC used `mode=ro`,
`query_only=ON`, and one transaction per database. They read session/command/job
identities and terminal metadata, not scripts, payloads, credentials, or raw
SQL parameters. The same seven unreleased pairs remain: all three Mac pairs,
the Linux pair, and all three Sandbox pairs. Every row is `lost` at session,
command, job, and teardown levels; the last command event is `command_lost`,
output is incomplete, and slot stop/release plus reservation cleanup/release
timestamps are null. Each marker below is mode 0600, singly linked, owned by
the recorded service UID, and matches its session and generation. No marker
contains `lost_recovery_cleanup_confirmed_at`.

| Host | Session → command → job | Recorded PID/PGID; start identity | Marker SHA-256 | Fresh process observation |
| --- | --- | --- | --- | --- |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` → `job-2b4d2e3c9682d54e89342f7a486b5ddd` | 12701/12701; `1792524938329971` | `582aa7f474104a738a03368664630d2d453855e896f944b8d9d419a7693e9c91` | direct read-only `ps` had no matching group |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-a66ce02cbaddf294e13afb3a409c317c` → `job-c40088894069e76f12650ea3bb0376bc` | 22367/22367; `1792525566416842` | `02862e07e173fa4d75071b821179bd3c48c552904d593d83af4a9ca4972ced93` | direct read-only `ps` had no matching group |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-b524b94074ee26855f1778d52c9655cf` → `job-d46393c75fc5a21dc2fa38a58329d2bc` | 54385/54385; `1792510335431810` | `d47128d18d241ab9c0364a08df609c4f89657a33cd5fe5b6c4c189926bfb4631` | direct read-only `ps` had no matching group |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` → `job-a73371770d4d29e2fc22fafd135396e9` | 118705/118705; `38701379` | `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e` | only UID-1001 zombie `bash <defunct>` |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` → `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe` | 2851086/2851086; `625457721` | `c932f38efb1695dfdc8f34e2e4663ad7ffcb2f74ea857a8443dbb4b86597ef49` | only UID-1001 zombie `bash <defunct>` |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-a06764547029ef15e4726ebd56a03cdf` → `job-3cb15f466008a37dc2bb719d83a573d4` | 3969302/3969302; `645723329` | `e060c9e338cb8d59da47bcae7dd1ec106098da6c2e89598072dbdb84fa4c8177` | only UID-1001 zombie `bash <defunct>` |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-8cf6db5bb3871e721133825520367598` → `job-77ea4fda0582a7e9d3e49d269830d6ba` | 3253996/3253996; `642533456` | `a49c560f83ebe81c1bf65e7b156f051f268dc60982bc063c1a5955b7f8d0d5` | only UID-1001 zombie `bash <defunct>` |
| Factory | none | — | — | no unreleased pair |

The inventory hashes remain as in the first table; the refreshed records still
do not supply durable runtime cleanup and capacity-release proofs. Do not
manually clear these seven reservations. The supported recovery installer
also installs artifacts, so budget and approve it as an installation action,
not cleanup-only. No pair was recovered, replayed, or released in this refresh.
