# CAT-06 legacy PRD compatibility evidence — 2026-10-22

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
