# SIMPLE-04 — Dev deployment and coexistence acceptance

**State: PASS — Dev deployment and coexistence acceptance complete.** The
native-cgo fix and local one-off command-read correction are delivered through
source `d9223e6c2342babbd2ed4fc217e9ba04932fd4fe`; full `make check build`
passed on Mac and all three workers. Four S9 Dev bundles were rebuilt from
synchronized revision `2204e18ecd6eddc916ffa39e75ac905d3dbe2851`, installed and
started only in Dev. Mac-local, direct and queued routes, managed-session
cancellation, requested timeout, explicit limit, mailbox/ACK and Dev restart
isolation passed. S10 restored the exact factory Dev direct ingress rule and
passed authenticated direct work. Fresh Mac PRD API and locald readiness both
returned ready with SQLite writes ready; the earlier `database_write_failed`
could not be reproduced and no PRD file, config, process, service or mailbox
was changed. Its exact historical SQLite cause was not retained in service
logs, and the restricted-shell `SQLITE_READONLY` result was not from the
service. See the S10 closeout below for details and remaining diagnostic
limits. QAS remains the next unit, SIMPLE-05.

## Scope and predecessor

This unit uses the active existing-host profile only: the existing Mac,
`linux-host`, `sandbox-host`, and `factory-sandbox`, with PRD unsuffixed and Dev
isolated under the `-dev` roots. It does not install QAS or upgrade PRD.

Predecessor `SIMPLE-03` evidence closeout E and the synchronized Mac/worker
revision are `c3c4683a4c3cbd0fc5c8944f212d7583be9d9a23`. The first Dev bundles
were built from that revision and remain preserved outside Git at
`/private/tmp/remote-session-runner-simple-04/`; their manifests bind:

| Host | Bundle SHA-256 |
| --- | --- |
| `mac-control-plane` | `33cb1fc6eb0f34cdc7f4944b817b0dc97b35d81f00a22ba0bad124dc99064977` |
| `linux-host` | `4cd1678e4d2bd2f42bfde970c1e077e9b2a746132cba270ac0e68e2d1ba369ca` |
| `sandbox-host` | `08fd681551e7cfe99cc7453db3473be227a7cddb541ae28b3c991c3e794a9523` |
| `factory-sandbox` | `5d102118ad95b69ca1a52ee4e4c52e6f90ac1ebf78c57ade8793418e6e7e2ce7` |

These E bundles are not the final acceptance candidate after the source fix;
the per-host bundles will be rebuilt from the corrected synchronized revision.

## Fresh preflight

On 2026-10-17/18 UTC, direct read-only SSH was used because the installed
unsuffixed Runner cannot safely validate its own Dev service setup, and mailbox
requests would create work in PRD. SSH used the configured explicit Mac
identities with strict host-key checking. No Runner request or ACK was
published.

All three PRD `runnerd.service` units were active and ready on `8443`, still at
installed revision `2802f00c93eb51a80f23b9380238c309545d6329`. Before Dev setup,
`linux-host` reported 20,877,616 KiB free and 502 MiB available memory;
`sandbox-host` 2,640,712 KiB free and 4,524 MiB available memory, with one
pre-existing session and no command/queue/backlog; `factory-sandbox` reported
44,562,988 KiB free and 576 MiB available memory. Dev and QAS roots and
8444/8445 listeners were absent on all workers. PRD health was ready on each.
No existing session was inspected, stopped, or changed.

The Mac Dev root was absent. The existing PoC CA was valid through 2027-09-25;
its public certificate fingerprint was
`8C:39:68:04:7E:F1:A9:6F:C1:35:86:70:31:AC:95:D8:65:41:8C:B0:AB:F8:99:8B:33:41:AC:C6:C9:83:65:82`.
Its owner-only private key matched that certificate. New Dev-only client and
server leaf keys/certificates and three Ed25519 bridge identities were created
under the task-owned temporary identity directory. Each TLS leaf has the
Dev/profile URI SAN, the server leaf also has its host public IP SAN, and
OpenSSL verified the client/server EKUs and chain. Leaves expire 2027-07-14.
The existing CA key, CA certificate and `poc-ca.srl` were not modified. Private
keys and certificates remain outside Git with mode `0600`.

## Observed first-start failure and correction

The first Mac Dev E candidate was installed, then started through the selected
lane controller. `runner-locald` became ready, but `runner-local` repeatedly
exited with:

```text
runner-local: configure service: validate configured mailbox work: mailbox configuration would abandon retained ingress
```

Read-only LaunchAgent/process/log inspection found the cause: Mac startup
validated/created the unsuffixed `config.MacServiceRoot` before loading the
selected configuration, and its legacy mailbox fallback compared a fresh Dev
database against the PRD default inbox root. This both rejected Dev startup
and violated the selected-root boundary.

Only the Mac Dev agents were stopped. Before stopping, Dev locald health showed
zero sessions, commands, queued work, and mailbox backlog. A read-only query of
the Dev database afterward showed zero `exec_sessions`, `exec_commands`, and
`mailbox_exchanges`, an empty mailbox configuration registry, and no files in
the Dev mailbox. Nothing was replayed or ACKed. The Dev database, config,
bundle, logs, credentials and mailbox directory were preserved.

The correction loads the selected config before validating/creating its root,
accepts only the three canonical Mac lane roots, and derives the mailbox
fallback from the selected config's default mailbox. The legacy V1/V2/PRD
fallback remains the unsuffixed root. Focused regressions cover the
canonical-root allowlist and per-lane default-mailbox baseline. It was delivered
in source revision `6d971c1a30bb202a55de9eb67b12471398fd993b`; Mac `make check`
passed, and the exact revision was pushed to Gitea, verified on the automatic
GitHub mirror, and fast-forwarded to all three clean worker checkouts.

The unsuffixed Mac PRD API and locald were ready immediately before the Dev
start at revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`, with one existing
session, no active commands/queues, and no mailbox backlog. A fresh post-retry
comparison will be recorded before claiming coexistence.

## Worker Dev install and first-start results

The immutable S1 worker bundles were rebuilt from synchronized source
`6d971c1a30bb202a55de9eb67b12471398fd993b` and validated by the installer:

| Host | Installed Dev bundle SHA-256 | Initial result |
| --- | --- | --- |
| `linux-host` | `d78e604e3d8c984003ff420c0bbfcb06c3bb9aee30aa658cb5c972451164f7e8` | Start reached 8444; initial-install marker cleared. Stop found no retained work, but the old log-capture wrapper ended with systemd `Result=exit-code`, `ExecMainStatus=255`. Dev is now stopped. |
| `sandbox-host` | `cc684f80d1c1b44bab11c98115047740ac92a9dfc7cdd9ba4ababb131c5e129e` | First `start` returned `ErrSimpleLanePreflight` while systemd had already started the service. Subsequent read-only checks showed startup reconciliation inspected zero sessions/commands, schema 39 and integrity `ok`, and 8444 listening; the initial-install marker remained. Guarded stop found no retained work, but the old log-capture wrapper ended with systemd `Result=exit-code`, `ExecMainStatus=255`. Dev is now stopped. |
| `factory-sandbox` | `3f77740feed775d018216e2f0fc34aa39d3abedc1b7714bf56eceea6767726e9` | Installed and enabled only `runnerd-dev.service`; not started. |

The worker bridge setup added one `runner-dispatcher-<host>-dev` key to each
existing `authorized_keys`; no sshd restart occurred. Current files have three
lines (the two pre-existing entries plus the Dev entry). Each preserved backup
hash matches the exact pre-change hash: linux-host
`261ffdee9b87325347104bce9c1b5f9aa48c25746e0a5316aecfdadf8cfa7369`,
sandbox-host `5a226ec9d5e152426cc37afc0b5376e2f028951a3643b151c0b3edf0549d4561`,
and factory-sandbox
`d711ac48a1097c28011758fd7d2e7e3c2a92ad560cce38f999cfb2ca5d04c129`.

No Dev request or ACK was published during setup. The installer-created Dev
database on sandbox-host contained no submitted work; startup reconciliation
reported zero work, and both guarded Dev stops completed their durable
quiescence proof. The pre-existing sandbox PRD session was not inspected or
changed. PRD stayed active on all three workers; its captured PIDs were 149913
(linux-host), 717823 (sandbox-host), and 50302 (factory-sandbox). A fresh PRD
health/version comparison remains part of acceptance closeout.

The startup failure was caused by a race: `runner-lane start` checked the
fresh database immediately after systemd became active, before Runner finished
creating/migrating it. The active process then initialized successfully, but
the failed first call left its install-progress marker. The source correction
now retries only the bounded initial database readiness proof and finalizes a
valid marker when a retry finds the service already active.

The stop failure was caused by `runner-log-capture` treating its own SIGTERM as
a context cancellation that killed the child immediately. That bypassed
`runnerd`'s graceful shutdown and reported a normal systemd stop as a crash.
The source correction forwards SIGTERM, waits up to 20 seconds for graceful
exit, bounds output-drain delay, and makes the lane controller require
systemd `Result=success` before recording stop/quiescence completion.

Focused startup/stop/log-capture regressions pass. The first full `make check`
attempt failed on a source compile error and the new shutdown regression
returned exit 78; both were fixed. Mac `make check` and `make build` then passed.
S2's production source changes were included in the corrected S3 candidate
described below; the S1 failure trials remain preserved as historical results.

The first worker `make check` on linux-host then exposed a non-hermetic existing
test: it expected the canonical Dev install root to be absent, but that host
now contains its S1 candidate. The test uses a fake service runner and made no
real systemd change; it observed the installed config/bundle and returned nil
instead of the expected missing-install error. The test now injects the
readiness failure it means to assert.

## Corrected source delivery and checks

S2 source commit `c868ecb87d2088043175358effebc4c81aa8470f` contains the
bounded initial-database readiness retry and graceful log-capture/systemd stop
proof corrections described above. It passed Mac `make check` and `make build`,
was pushed to primary Gitea, and its exact SHA was verified on the automatic
GitHub mirror before all three clean worker `dev` checkouts fast-forwarded to
it. The first linux-host `make check` at S2 exposed the hermeticity issue above.

S3 test-only commit `d7b4f073d1d7a5924249ba827a7697ef384a54d6` injects the
readiness failure in that regression instead of depending on an absent real
installation. It was pushed to Gitea, the exact mirror SHA was verified, and
all three clean worker checkouts fast-forwarded to the same SHA. Mac `make
check` passed at S3. Full `make check` then passed on linux-host, sandbox-host,
and factory-sandbox at the exact S3 SHA; each checkout was clean on `dev`.
`make check` covers test, vet and smoke/build-help checks. The separately
required Mac `make build` passed on S2, whose production source is identical to
S3. No worker service was started or changed by these source gates.

## S3 candidate installation and first route checks

From clean synchronized source `6eaf953e1ae29179f3c31458587a2081c9e529e7`,
the per-host S3 bundle manifests in
`/private/tmp/remote-session-runner-simple-04-S3-6eaf953/` bind:

| Host | Bundle SHA-256 | Guarded update result |
| --- | --- | --- |
| `mac-control-plane` | `ab4b974bb57e94bcb3e2d9c70f6ae6fc45479b5918db4c5399e9c30c73efd92a` | Installed and started; prior Dev backup at `~/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-1375356866` |
| `linux-host` | `01317ceebc093d42c6e782784194dda7bc359ce20436afc83822de23cebb8764` | Installed and started; prior Dev backup at `.../remote-session-runner-dev/backups/.simple-backup-3115583341` |
| `sandbox-host` | `d81a43decad5cde1e314905581a2ec2bff97374efde664a105289c4769a5838a` | Installed and started; prior Dev backup at `.../remote-session-runner-dev/backups/.simple-backup-1788961269` |
| `factory-sandbox` | `33842fd77830abee0cf2fb457765f921af704611584a3e04098a1964f2e8153f` | Installed and started through the matching initial-install marker and exact existing config hash `7af6d8ea68764f703b7aebd40966bbc7ff8b86523a2cb948d5f60e68659cd727`; first attempt refused without `--config-file`, made no service/config change, and retry succeeded |

Each manifest records source `6eaf953e1ae29179f3c31458587a2081c9e529e7`,
Go 1.27.1 and its exact component digests. Linux and sandbox updates passed the
installer's zero-work proof and created consistent backups. Factory's initial
marker identifies `factory-sandbox/dev`; its installed config hash exactly
matched the owner-only config supplied to the retry. Mac update passed the
selected-lane stop/quiescence proof and created its backup. All four services
then started through `runner-lane`; worker systemd `Result=success`, with Dev
PIDs 2536857 (Linux), 807237 (sandbox), and 1146464 (factory). Each listens on
its private interface at 8444. PRD remained active on 8443 with unchanged worker
PIDs 149913, 717823, and 50302.

Fresh Mac PRD API/locald health is ready at installed revision
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`; it retains its pre-existing one
active session, zero active commands/queues, and zero mailbox backlog. A single
PRD locald read timed out once, then a bounded curl recheck returned ready with
the same build; the LaunchAgent remained running. Mac Dev API/locald are ready
at S3 with zero active sessions/commands, queues or backlog. All three queued
SSH `ping` frames returned a correlated result after repairing the Dev-only
known-host lookup. The original pinned `[IP]:22` entries remain; the plain-IP
aliases use the same configured key SHA-256 values `7b3c6924…`, `9f68b617…`,
and `47c9a62a…`. No PRD trust or `authorized_keys` entry changed.

Direct mTLS health returned HTTP 200/ready at S3 for Linux and sandbox. The
actual Runner CLI one-off probes also succeeded:

| Route | Job / session / command | Result |
| --- | --- | --- |
| Direct `linux-poc-dev` | `job-976af28bbb1788dee98c0db74829a169` / `sess-328b3aca631cadfb69c4803ae7f81834` / `cmd-a82ad2a211c85395f30105a99a2b4af4` | exit 0; output complete, not truncated; terminal sequence 5; teardown closed |
| Direct `sandbox-poc-dev` | `job-acd68db77dd84be128292821c4ca47a4` / `sess-53bfd4efc90fe41a18a3337d69fbc5bb` / `cmd-6968a35766006e6bbfe3d798299dcc8f` | exit 0; output complete, not truncated; terminal sequence 5; teardown closed |

The queued Linux one-off (`simple04-20261018-linux-queued-01`, job
`job-46f79ce61446217a1d80f81ddde1ce52`, session
`sess-da7ce4093861ad6a29fc4ba18c4542ef`, command
`cmd-e5bb8ba2d097b3241ad9d24097f7606f`) was accepted once and completed with
exit 0 and closed teardown. The CLI then hit a 404 while reading its event
history. Read-only Mac Dev database inspection confirmed the reconciled `run`
intent and all five durable events, including both output records and
`command_succeeded`; nothing was replayed or ACKed. Direct reads from the target
were correctly rejected with controller mismatch. The active source correction
uses the existing owner-bound `GetLocalIntentByCommand` lookup for command and
event reads, covering both `submit_command` and one-off `run`. Its regression
reproduced the 404 before the change and passes after it. Resume only this exact
accepted job with its original idempotency key after delivery to prove the
complete CLI read path without rerunning the script.

The public `8444` route connected for Linux and sandbox. Factory's Dev process
is listening on `10.0.0.30:8444`, while public `145.241.255.66:8444` returns
connection refused; `22` and PRD `8443` connect. The factory host INPUT policy
was previously observed `ACCEPT`; no host or provider firewall rule was
changed. The cloud ingress path still needs read-only inspection through a
provider-authorized operator. OCI CLI/config were not available on the workers.
This blocks only factory direct acceptance; queued factory health ping passes.

## S4 Dev candidate and current S5 correction

Source `4147cfad3d839f8744fed06babeb507ca2c90dd5` passed Mac `make check` and
`make build`, was pushed to Gitea, verified at the exact GitHub mirror SHA, and
fast-forwarded to the three clean worker `dev` checkouts. Their `make check`
gates passed at that revision. Candidate bundles were built from that source
and installed only to Dev through `runner-lane`; all four Dev services started
and their manifests matched the candidate build.

| Host | Installed S4 bundle SHA-256 | Guarded Dev backup | Current Dev process |
| --- | --- | --- | --- |
| Mac | `8f0419b1b2dc4b8dae8591e5d5dc04baefdf90c79099889073ff01c61c1fe0a4` | `~/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-3800986350` | `runner-local` PID 44544; `runner-locald` PID 44613 |
| `linux-host` | `af8c1182920e2c5283821116569fe5e901f5015730bc4cc89570bfe1e7b4709e` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-2414843732` | Dev PID 2564104 |
| `sandbox-host` | `94fe65e9dd70ca8a3ec399c32036374209d7ec362c767557a9a7a2268e836c29` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-1940518023` | Dev PID 840416 |
| `factory-sandbox` | `d573f124b099d0b1d20ce67fdd49f33fc9c225e75382e2f4bd68c6bfb7ca397d` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-424317290` | Dev PID 1161689 |

The worker archives were verified before transfer: Linux
`efcebbfb7e305c01278398541ac417e31604b460fb863e45d03b3f5e1076e416`, sandbox
`cf70c6a2083f17e3b47538e2bcf5ef2a0824608a7a0a2820d48642f7615f4e08`, and
factory `69eb915a4b9f950ae3ea7b97cc76062290711d0428a4152b67c31c0035889ef7`.
All three worker Dev services report successful startup and listen on the
private `8444` interface. Unsuffixed PRD remained active at its previously
recorded PIDs `149913`, `717823`, and `50302`; no PRD binary, configuration,
service or database was changed.

S4 acceptance completed these bounded probes with complete output, one terminal
event, and successful teardown unless stated otherwise:

| Route | Job / session / command | Result |
| --- | --- | --- |
| Queued Linux, retained request `simple04-20261018-linux-queued-01` | `job-46f79ce61446217a1d80f81ddde1ce52` / `sess-da7ce4093861ad6a29fc4ba18c4542ef` / `cmd-e5bb8ba2d097b3241ad9d24097f7606f` | Succeeded, exit 0; S4 event lookup returned all five events and complete history; no script replay |
| Direct `linux-poc-dev` | `job-8bba2604fca5ecb1d88f14706ff21dd3` / `sess-dfeb9d39108d0cbe630e11b03a528683` / `cmd-05d2f7fac5c45e6bcf5ff115e7ff0e03` | Succeeded, exit 0; output complete; teardown closed |
| Direct `sandbox-poc-dev` | `job-4298952daef2affb9596811957d8e6b3` / `sess-4371e20ab93f0a6a4df9f34e9744ff55` / `cmd-15df45a26180c1a0eed419eafea6667b` | Succeeded, exit 0; output complete; teardown closed |
| Queued sandbox | `job-f916c22588212dcd109bbb4f38a5787e` / `sess-56822a3c46ad2b36ced0a61b881b9692` / `cmd-de31df203505602866e2ad517da4ac2d` | Succeeded, exit 0; full history confirmed on follow-up read; teardown closed |
| Queued factory | `job-090fd8285dea535f1d6f3f5e78cfce2d` / `sess-3fb9aa0ca39273b101b7e274e4bdb289` / `cmd-cf99892bf18a2dc594464c58c61ed99a` | Succeeded, exit 0; full history confirmed on follow-up read; teardown closed |

The factory direct route remains blocked: TCP `145.241.255.66:8444` returned
connection refused while Dev was listening on `10.0.0.30:8444`; factory queued
work succeeds. The host INPUT policy was previously read as `ACCEPT`; no host
or provider firewall rule was changed. The required external action is a
provider-authorized inspection and, if absent, a scoped ingress rule for Dev
TCP 8444 from the approved test sources. PRD TCP 8443 and SSH 22 remain
reachable.

Mac-local acceptance has not passed. Three uniquely keyed S4 requests failed:
one-off `job-458e151ce1510ef1bf9e096b07a3b178` / session
`sess-b683019b2b00469e302c66ddc134052` / command
`cmd-22699cc5071055d1112b0a6321b90d4d`; managed-create intent
`intent-8a222c299360ec3827db496aa7573e6d` / session
`sess-90618db1ef707bdacd0b7fd02f00949a`; and fresh one-off
`job-cc74ecbb6b6d1bcd0f0fd8f1a1838b9c` / session
`sess-5cf5289e5762c14d8999d5e83036f120` / command
`cmd-bfe6f9ce056a3776788e1f56f81bfda2`. The first and third jobs ended
`failed`, command state/exit code unknown, event cursor 0, and teardown pending;
the managed create was recorded `not_delivered/locald_rejected`. Read-only
Dev health after the retry shows zero active session/command slots, queued work,
mailbox backlog, storage errors, and cleanup failures. No failed request was
replayed. A temporary isolated runtime diagnostic using the actual Dev account,
configuration, workspace root and LaunchAgent-like environment passed
create/run/close, so it does not explain the installed-process rejection. S5
adds a Dev-local sanitized runtime-failure log for the next fresh acceptance;
the rejection cause remains unresolved until that candidate is installed.

Managed Linux create/status/run/read passed. The cancellation probe for command
`cmd-d8a0ee3df35bae8e460ef1758a57ea5e` returned a signal-style exit `-126` and
was incorrectly recorded as `command_failed` while cancellation was in
progress. The session then closed with `teardown_outcome=closed`; the active
source correction defers negative signal-style completion to the cancellation
path, which must prove the stop/output boundary before recording
`command_cancelled` or `command_lost`. Regression coverage also preserves the
existing successful-completion-wins race. The cancellation regression and full
Mac/worker source gates were rerun at S5 after the diagnostic change and passed;
the S5 candidate installation and remaining live acceptance are recorded below.

### S5 source delivery and candidate installation

S5 source `3c60844cc4c55b5fa9d12c9405855deca13cec23` was committed on Mac,
pushed to Gitea `origin/dev`, verified at the exact GitHub mirror SHA, and
fast-forwarded to all three clean worker `dev` checkouts. `GOMAXPROCS=2
GOFLAGS=-p=1 make check build` passed on Mac and each worker at that revision;
the focused `TestP137` race regression passed on Mac. The S5 source correction
defers negative signal-style command completion while cancellation owns the
command and records the safe Mac-local runtime-unavailable cause in Dev locald
logs.

Per-host S5 bundles were built from that exact synchronized revision and
verified by the guarded installer:

| Host | S5 bundle SHA-256 | Install result / backup |
| --- | --- | --- |
| Mac | `412b506a542a53a0f819be72c2aad5c1f7ae79cfdbfd69258e862ae82d6adb2a` | Install on 2026-10-18 (Europe/Warsaw) failed safely because `launchctl` still reported the selected agents active immediately after `bootout`. The current pointer remains S4 `8f0419b1b2dc4b8dae8591e5d5dc04baefdf90c79099889073ff01c61c1fe0a4`; Dev agents are now unloaded. No S5 backup was created and no PRD agent was targeted. |
| `linux-host` | `b2dca8885906806f16f6367463f64fc908231c56cb3b155fc929161e79009645` | Installed/started; previous S4 bundle `af8c1182920e2c5283821116569fe5e901f5015730bc4cc89570bfe1e7b4709e`; backup `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-2980697414` |
| `sandbox-host` | `fa154065ee5909cd86bc1656349d3642a642a5036cde5bdc20d914502a1912f0` | Installed/started; previous S4 bundle `94fe65e9dd70ca8a3ec399c32036374209d7ec362c767557a9a7a2268e836c29`; backup `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-864389217` |
| `factory-sandbox` | `dd2cde775f3c26631ae67e2cf68a773ac3ff159bb2ddfa89c18fd42048555318` | Installed/started; previous S4 bundle `d573f124b099d0b1d20ce67fdd49f33fc9c225e75382e2f4bd68c6bfb7ca397d`; backup `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-3384430131` |

The failed Mac stop is an observed asynchronous-unload race in
`SimpleServiceController.Stop`: `launchctl bootout` returned successfully, but
the immediate status check raced the unload and caused a false failure. No
manual process termination was used. The source correction in this commit polls
only the selected Dev agents within the existing bounded stop context and then
requires the normal stopped-lane quiescence proof. Its regression simulates
delayed unload for both agents. Focused `TestSimpleMacServiceController` passed;
`GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passed on Mac after this change.

### S6 source delivery and worker validation

The installer fix was delivered as source commit
`2b50bf761e60bad9e49353ef9afc08e6de6e94b2` on `dev`, pushed to primary Gitea,
and verified at the same GitHub mirror SHA. Before pull, all three worker
checkouts were clean on `dev` at S5 `3c60844cc4c55b5fa9d12c9405855deca13cec23`.
Each fast-forwarded to S6 and ended clean at the exact S6 HEAD.

`GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passed on Mac, linux-host and
sandbox-host at S6. It passed on factory-sandbox on the third run. The first
factory run passed tests and vet but smoke linking failed with `disk quota
exceeded`; measurement showed `/tmp` is a 476 MiB tmpfs with 193 MiB available,
while the existing root filesystem had 43 GiB free. The second retry used a
long task-owned `TMPDIR` under `/home/ubuntu/.cache` and exposed a Unix socket
path-length limit in `TestLanesL306NativePeerEvidence`. The final retry used a
short unique directory from `mktemp -d /home/ubuntu/.cache/r.XXXXXX`, ran the
same full command serially, passed tests, vet, smoke and build, and removed only
that task-owned temporary directory through an exit trap. No shared cache was
deleted or replaced.

## S6 candidate refresh, coexistence postflight, and Mac-local attempts

The previous Mac installation had been blocked by an asynchronous `launchctl`
unload race. S6 added a bounded wait for the selected Dev agents to disappear;
the delayed-unload regression passed. After delivery of S6 and the evidence
checkpoint, candidates were rebuilt from exact synchronized source
`6c4aa9ef73493ac513ef6897c6f414d7f7dc7b3e`, checksum-verified, and installed
only into Dev through the guarded lane installer:

| Host | S6 candidate SHA-256 | Guarded install backup |
| --- | --- | --- |
| Mac | `1eba56e845a81161d8c9233a39b194a063ab582a901a88a22c41780c9aa2e9d3` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-1884196807` |
| `linux-host` | `9c5b48005af1cefb67bb907fe10ff25dc3f0eadc1ca9aece5abf673225731bf1` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-2525353079` |
| `sandbox-host` | `55892548c0939e2ebbf832eca4eaf0efee95fcff32b649aafbbd384ca1dab7fc` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-2737904280` |
| `factory-sandbox` | `cca42aa81dc1257be59060e4d0553cc23f600b6ccf378fe89c9ba582f3afb094` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-103309046` |

All four Dev services started. Fresh health showed each ready at build revision
`6c4aa9ef73493ac513ef6897c6f414d7f7dc7b3e`, with zero active sessions,
commands, queued work, or mailbox backlog. PRD remained ready and unchanged:
the three worker PIDs remained 149913, 717823, and 50302 at installed revision
`2802f00c93eb51a80f23b9380238c309545d6329`; Mac `runner-locald` PID 48627 and
`runner-local` PID 48708 remained at revision
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`, with its pre-existing one retained
session. No PRD process, binary, configuration, database, mailbox, or firewall
was changed.

Two fresh Mac-local attempts are retained distinctly:

* The first used the wrong environment `mac-dev` for target/profile
  `local/mac-workstation`: job `job-b72101410e94953fcbcc432207961a5d`, session
  `sess-d0d1a77011a0089397ae72b51488aafb`, command
  `cmd-59fd69b47446bf59bdff51f64f0ec69e`. The intent was locally accepted, but
  environment authorization rejected it before a session or command row was
  created. It is an invalid test input, not a runtime acceptance result; it was
  not replayed.
* The second used the configured environment `mac-workstation-dev`: job
  `job-2d035e5075f245945b8247a8ab456e0f`, session
  `sess-480ac1f7bd2e20f38283d93b1c183a8c`, command
  `cmd-25092c53343bf5ebb0ede9ad4314a0df`. The job and session reached terminal
  failed state during runtime startup, before a command row or command event
  existed. No unreleased session or command reservation remained, and Dev
  health returned to ready with zero active work. Existing owner-only
  `locald.log` recorded the allowed run authorization but had no worker failure
  diagnostic, so the underlying runtime error remains undetermined. This job
  was not replayed and no ACK or manual cleanup was performed.

The S7 source change logs only bounded Mac-local empty-source runtime
startup failures, with stable job/session IDs and bounded error text; it does
not log the script. An integration regression drives an accepted local one-off
through the queue worker and proves the failure is logged, creates no command
or live reservation, and is not retried after terminalization. Focused
queueworker tests pass. Mac `GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passes
with normal process-test access; the sandboxed attempt could not execute
`/bin/ps` in an existing Mac process regression, then the unrestricted local
test gate passed. Source delivery and synchronized worker validation for this
diagnostic change are recorded below.

### Diagnostic source delivery and worker gates

Source commit `57e11ab7b050c7028f501929a4b2b6a96a6179fb` was committed on Mac
`dev` and pushed to primary Gitea `origin/dev`. Direct `ls-remote` reads of
Gitea and automatic `github/dev` both resolved to that exact SHA. Git's push
could not refresh the local `origin/dev` tracking ref because `.git` metadata
is read-only in this execution environment; the authoritative remote refs were
verified directly and both matched. No working-tree changes remained.

Before pull, each Ubuntu checkout was clean on `dev` at
`6c4aa9ef73493ac513ef6897c6f414d7f7dc7b3e`. Each used its configured explicit
administrative SSH identity and the checkout's GitHub mirror identity to
`git pull --ff-only origin dev`; all ended clean on the exact source SHA above.
`GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passed on `linux-host`,
`sandbox-host`, and `factory-sandbox`. Factory used a unique short
`/home/ubuntu/.cache/r.XXXXXX` TMPDIR with an exit trap to remove only that
task-owned directory; shared Go caches were retained. The diagnostic candidate
rebuild exposed the Mac cgo build defect described next.

### S7 Mac candidate and cgo failure

All four S7 candidate bundles were built from synchronized revision
`e357bd7d5910d34f16cf95d43ecd6a7027870eca` and checksummed:

| Host | S7 bundle SHA-256 | Result |
| --- | --- | --- |
| Mac | `3ecc084b7dc8d36a3e94eb67718595fede1aa874586709e646d998c10de73871` | Installed to Dev, then stopped after the failed probe |
| `linux-host` | `ae9773da886c47781cc13d1a511247baeae5042c62dadafc6614820179231c6a` | Built only; not transferred or installed |
| `sandbox-host` | `aea0b1fb57c1608e71f9d12d6902729d2c62a359f784bbf0230d263a46b647f3` | Built only; not transferred or installed |
| `factory-sandbox` | `69681916a05f17fea3126004dafe7ff5f9fbdf974346f2ecc3b1aae55a7f8de1` | Built only; not transferred or installed |

The Mac guarded installer created backup
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-2072801282`,
replacing prior S6 bundle
`1eba56e845a81161d8c9233a39b194a063ab582a901a88a22c41780c9aa2e9d3`. Mac Dev
started at revision `e357bd7d5910d34f16cf95d43ecd6a7027870eca`; health initially
reported ready and zero active/queued work.

One fresh configured Mac-local probe used environment `mac-workstation-dev`
and target `local/mac-workstation`: job
`job-301e1538de1dc172a4233cc1faff5508`, session
`sess-422d3be2b7a163548cd878bfafe0e680`, command
`cmd-fac5705ca3dfb184bb5fc99633094888`, key
`simple04-20261018-s7-mac-local-03`. It failed before command creation. The
new owner-only locald diagnostic recorded:

```text
runtime unavailable: record Mac runtime ownership: Mac process start identity requires Darwin with cgo (pid 14059)
```

The cause was `simpleBuildEnvironment` forcing `CGO_ENABLED=0` for every
platform, despite the Mac process identity implementation using Darwin libproc
through cgo. The durable job and failed session remain preserved; no command
row, command event, active slot, or session reservation remained. The Dev
doctor reported ready with zero active sessions, commands, queues and mailbox
backlog before the lane was stopped. No request was replayed and no ACK was
published. The guarded `runner-lane stop --host mac-control-plane --lane dev`
completed its quiescence proof and stopped only Mac Dev; PRD remained running.

The S8 source correction enables cgo only for Darwin bundles, requires the Mac
bundle to be built natively on the target Darwin architecture, and adds a
platform-environment regression. Focused laneinstaller tests and the full Mac
gate pass. Source delivery and worker validation are recorded below. The
native-cgo candidate is installed on Mac Dev; its accepted job must be read
again after the command-read fix is installed. Worker candidate refresh and
the remaining Dev acceptance are still pending.

### S8 source delivery and worker validation

Source commit `6a108303522c915a68ca48754448c3b6bcc89fe1` was committed on Mac
`dev` and pushed to primary Gitea. Direct `ls-remote` checks on 2026-10-18
resolved both Gitea `origin/dev` and automatic `github/dev` to that exact SHA.
The three worker checkouts were clean on `dev` at that exact revision after
fast-forward delivery. The final read-back after testing confirmed each worker
remained clean at the same SHA.

The required full gate `GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passed on
`linux-host`, `sandbox-host`, and `factory-sandbox` at S8. Mac passed the same
full gate before S8 was committed. Commands ran on the named worker via its
configured explicit administrative SSH identity, inside the synchronized
checkout; the Makefile selected Go 1.27.1. Factory used a task-owned short
`/home/ubuntu/.cache/r.XXXXXX` `TMPDIR`, removed by an exit trap; shared Go
caches were retained. No host service was changed by these gates.

The first sandbox full-gate attempt failed in
`TestLanesL311LedgerGrantSerializesCasAndPreservesRevocationSets` with a TLS
fixture error stating that the current test server certificate was no longer
active. The isolated test was rerun once and passed; the complete sandbox
`make check build` was then rerun and passed. This transient failure and its
successful reruns are preserved rather than counted as a pass on the first
attempt. Linux, sandbox, and factory all ended clean at S8. The corrected
native-cgo bundle was then built and installed to Mac Dev; its outcome and the
newly required local command-read correction are recorded next.

### Mac one-off command-read correction and current Dev state

The S8 native-cgo candidate was rebuilt from synchronized
`afa369984f9e30709917a3c1f9c1d1add7d1cf28`, validated as Darwin/arm64 with Go
1.27.1, and installed only to Mac Dev. Its manifest bundle SHA-256 was
`308bc28754e09f25d1c149636aed031f7873f217283f0dff374adc24693c2398`. The
guarded installer preserved the prior Dev bundle at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-3324634733`.
The first `runner-lane start` through the sandboxed shell failed at launchd
bootstrap with I/O error 5 before either Dev label loaded. Read-only checks
confirmed valid plists, correct arm64 binaries and unchanged PRD agent PIDs.
The same guarded start through the authorized Mac shell then succeeded. Dev
locald and API reported ready at `afa3699` with zero session/command slots,
queued work and mailbox backlog. PRD stayed ready at its installed revision;
its Mac locald/runner PIDs remained `48627` and `48708`.

One fresh local run used idempotency key
`simple04-20261018-s8-mac-local-02`, environment `mac-workstation-dev`, target
`local/mac-workstation`, and produced job
`job-e9aa7949af21cdf327f2b7a5674740f8`, session
`sess-88ca6b02285140e85bedd70b788faeba`, command
`cmd-eb0eca9387233911337738d15d2b4cbb`. The CLI received HTTP 503 while reading
events at cursor 0 and explicitly reported that it had not cancelled or
restarted the command. No second request was sent. Read-only SQLite inspection
showed the durable job complete, command `succeeded` with exit 0, complete
untruncated output, terminal event sequence 4, closed session teardown, and a
released command slot. The event history contains one start, one stdout record
(30 bytes), and `command_succeeded`; no duplicate execution occurred.

The failure was an identity comparison error. A local `run` intent hashes the
whole run request, while its command row correctly hashes a separate
`submit_command` payload. The local command GET, events endpoint, and mailbox
command snapshot incorrectly required those hashes to match. Source
`d9223e6c2342babbd2ed4fc217e9ba04932fd4fe` allows the hash difference only for
`run` intents, uses command/session/script identity for those reads, keeps
ordinary submit-command hash checks strict, and lets mailbox command snapshots
resolve either supported intent operation. Its regression proves all three
read paths return the completed command and still fail closed on a script
mismatch.

The correction was pushed to Gitea `origin/dev`; direct reads confirmed the
exact same SHA on automatic `github/dev`. All three workers fast-forwarded from
clean `afa3699` checkouts and ended clean on the source SHA. Mac,
linux-host, sandbox-host and factory-sandbox each passed
`GOMAXPROCS=2 GOFLAGS=-p=1 make check build` on that revision. Factory used a
task-owned short `TMPDIR` under `/home/ubuntu/.cache`, removed by its exit
trap; shared Go caches were retained. The read-resumption test against the
actual retained Mac job remains pending installation of the corrected bundle.

The fresh preflight before the Mac Dev candidate refresh found all three
worker PRD services active and ready at installed revision
`2802f00c93eb51a80f23b9380238c309545d6329` (PIDs `149913`, `717823`, `50302`).
Worker Dev services were ready at `6c4aa9ef73493ac513ef6897c6f414d7f7dc7b3e`
with zero work. Sandbox PRD still showed its existing one active session slot
and `cleanup_failures_total=1`; no PRD session or service was touched. Mac PRD
was ready at `77910358a149d6e8fcdf4694498ebdf6c1f07f58`, retaining its existing
one session slot and zero active commands, queues and mailbox backlog. No PRD
binary, configuration, process, database or mailbox was changed.

The pending-work instructions in this earlier snapshot were superseded by the
S9 execution record below. No held ACK was touched. Do not mark SIMPLE-04 PASS
until the remaining direct factory route and PRD health issue are resolved and
all final postflight gates pass.

No PRD service, configuration, mailbox, credential or host firewall change was
made. Existing SSH authority changes are limited to the Dev forced bridge
entries and Mac Dev known-host aliases; the original worker SSH authority
backups remain verified above. The S9 section records the timestamp-only PRD
health-probe write and the later health-check failure separately.

## S9 install and acceptance — 2026-10-18

### Candidate source, transfer and selected-lane installation

The Mac source checkout was clean on `dev` at
`2204e18ecd6eddc916ffa39e75ac905d3dbe2851`. Gitea `origin/dev` and automatic
GitHub `github/dev` both resolved to that exact SHA. All three worker checkouts
were clean on `dev` at the same SHA before this evidence update. The source fix
is `d9223e6c2342babbd2ed4fc217e9ba04932fd4fe`; `2204e18` is evidence-only and
does not change candidate code. The full `GOMAXPROCS=2 GOFLAGS=-p=1 make check
build` gate had passed on Mac and all workers at `d9223e6`.

Four immutable bundles were built once from synchronized `2204e18` with Go
1.27.1. Worker archives were copied only as install artifacts, not as source,
and their SHA-256 values were verified before extraction. Installation used
`runner-lane install --host <host> --lane dev --bundle-dir <candidate>`; the
guarded installer stopped only Dev, proved retained work quiescent, took the
listed backup, and activated the manifest. `runner-lane start` started only
that Dev service.

| Host | Bundle SHA-256 | Worker archive SHA-256 | Guarded backup | Final Dev state |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | `0cbaff313de36ebe030144dd4192a1138aebb4b0ad54ca6fe93d6d0ff196f757` | — | `~/Library/Application Support/RemoteSessionRunner-dev/backups/.simple-backup-4005341277` | Active; health ready at `2204e18`; zero active slots, queues and mailbox backlog |
| `linux-host` | `3826c1406efcb9cd240e9dab55df5b09f03035d8537e85036033ad2476d38b4c` | `b7c66239c21878f76fcad6c1781dd21af06ea29b3603f03c4573f52625669110` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-3636333827` | Active; direct mTLS health ready at `2204e18`, zero active slots/queues/backlog |
| `sandbox-host` | `6ab39974eb78cf78333a048b683a365b954d95894d785d00ef96681cb8f74917` | `72d0ac6993f934533419b5e346ab45ed72306f18b8b3700150e44eb501852859` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-984644557` | Active; direct mTLS health ready at `2204e18`, zero active slots/queues/backlog |
| `factory-sandbox` | `d1b2399ed1af742afe6b4ffe01fd5d0e103d1efb3db7e12b41837a4e84cab37a` | `44b7b18cfb3516e48eecc4a2a12905bd2e88666f4891672b9a343ca92f4c9ec9` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-1892222392` | Active; queued bridge succeeded after restart; public direct ingress remains blocked |

The Mac current manifest file SHA-256 is
`88e82159e2719bb510fe306b540464c94270866cb3fc120e696f0360845b617a`. Each
worker `current` symlink resolved to its bundle-SHA release directory; each
manifest records source `2204e18`, Go 1.27.1 and the expected architecture.
Final worker checkouts were clean at `2204e18` before this evidence edit.

On each Ubuntu host, `runner-lane` was built from the synchronized checkout
using its explicit Go toolchain and a task-owned temporary directory removed
by an exit trap. The command form was:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go build -o "$task_tmp/runner-lane" ./src/cmd/runner-lane
"$task_tmp/runner-lane" install --host <host> --lane dev --bundle-dir <candidate>
"$task_tmp/runner-lane" start --host <host> --lane dev
```

Mac install and restart used the already-authorized Mac shell route because
the sandbox could not start its LaunchAgents (I/O error 5) and could not open
the Dev mailbox ingress lock (`operation not permitted`). The first mailbox
attempt failed before publication; a read-only search confirmed no files for
that request ID. The same native client test then passed through the authorized
Mac shell. A standalone curl attempt for explicit limits failed before HTTP
because curl rejected the existing certificate SAN syntax; no request was
accepted. The successful explicit-limit probe used Runner's pinned mTLS client
and did not weaken certificate validation.

### Candidate acceptance evidence

Every successful probe used a unique Dev idempotency key except the deliberate
same-key resumption of the already-completed Mac job. No script was replayed;
only the explicitly managed long command was cancelled. No historical request
or held ACK was changed.

| Path/check | Stable evidence | Result |
| --- | --- | --- |
| Mac-local run and same-key resumption | `simple04-20261018-s8-mac-local-02`; job `job-e9aa7949af21cdf327f2b7a5674740f8`; session `sess-88ca6b02285140e85bedd70b788faeba`; command `cmd-eb0eca9387233911337738d15d2b4cbb` | Original `run` and `events` reads returned the same IDs, one start, terminal sequence 4, exit 0, complete output, and closed teardown. No second command start. |
| Direct Linux Dev | `simple04-20261018-s9-direct-linux-01`; job `job-7fa3b98cb7284f20b555b3dc7054b71d`; session `sess-3489743d94bc84737943a2c252310c3c`; command `cmd-2963cc9729ac36993f245b7877bde82e` | Succeeded, exit 0, complete event history/output, closed teardown. |
| Direct sandbox Dev | `simple04-20261018-s9-direct-sandbox-01`; job `job-bd7924bb672c8fe0ce8b01e122f083d3`; session `sess-72f60f0a4620f253aba574a21ea7f81c`; command `cmd-76f1949ae0f2c3d572913505084823f9` | Succeeded, exit 0, complete event history/output, closed teardown. |
| Direct factory Dev | `simple04-20261018-s9-direct-factory-01` | Failed before HTTP admission: `dial tcp 145.241.255.66:8444: connect: connection refused`; no job/session/command ID was returned. This is the remaining factory direct-ingress gate. |
| Queued Linux Dev through Mac local ingress | `simple04-20261018-s9-queued-linux-01`; job `job-46810441296ab058531f7b7ef8bfe57e`; session `sess-a6727b369ff8c72f11e9c2d662d0d595`; command `cmd-c4906b4786f74d5e3059fca5da8e2b8b` | Succeeded, exit 0, output complete, closed teardown; a follow-up events read reached cursor/final sequence 4. |
| Queued sandbox Dev through Mac local ingress | `simple04-20261018-s9-queued-sandbox-01`; job `job-92242806e64dee192ef497228897b0d4`; session `sess-a0e1a96a7e42298386b1f2e08ff0c020`; command `cmd-bed1b1086d110d479832997748da7b96` | Succeeded, exit 0, output complete, closed teardown; follow-up events read reached cursor/final sequence 4. |
| Queued factory Dev through Mac local ingress | `simple04-20261018-s9-queued-factory-01`; job `job-b5ef41fd59c8db3ef715dcadd6d114c8`; session `sess-2864980bef7b3b427f5089845cc647b4`; command `cmd-59f11c242570929fea6fa4e32f16e4e0` | Succeeded, exit 0, output complete, closed teardown; follow-up events read reached cursor/final sequence 4. |
| Five-second requested target timeout on Linux Dev | `simple04-20261018-s9-timeout-linux-01`; job `job-52eb13d8961a1a16a12555c9a4452cf7`; session `sess-4b51855b8ad60c155db0d306b2bbcdc3`; command `cmd-da19b26ff5ca525ba770225bf7c128e8` | A ten-second sleep with `timeout_seconds: 5` ended `command_timed_out`, final sequence 3, output complete/not truncated, closed teardown. Temporary probe used the repository's pinned mTLS client and was removed. |
| Permitted explicit output limit on Linux Dev | `simple04-20261018-s9-limit-linux-01`; job `job-ad3e65f0c7ca1da49900b9d243588bfb`; session `sess-291564fb4778f9a87ab8640b71acbbb9`; command `cmd-95fffadffc4f4c97d7461c33a3a572cd` | `limits.output_bytes_per_command: 1024` was accepted; harmless command succeeded, exit 0, complete/not-truncated output, final sequence 4, closed teardown. |
| Managed Linux Dev session | Session key `simple04-20261018-s9-managed-linux-01`; session `sess-b013a7366c72822810adbdbd365d9606`; harmless command `cmd-88e9f2389e1f3caf097b31bd807aefd1` | Create returned ready; status read returned authority state; harmless command succeeded with complete output and final sequence 4. |
| Managed cancellation and close | Command key `simple04-20261018-s9-managed-linux-cancel-command-01`; command `cmd-f338667071305b02be21f77d4e9cfd5d`; cancel key `simple04-20261018-s9-managed-linux-cancel-01`; close key `simple04-20261018-s9-managed-linux-close-01` | Bounded `sleep 30` was explicitly cancelled; terminal event `command_cancelled`, final sequence 3, complete output; graceful close and subsequent status both reported session closed. |
| Dev mailbox request/read/ACK | Request and idempotency key `simple04-20261018-s9-mailbox-dev-linux-01`; inbox `default`; job `job-dd1dcfd24dc909c48bed88da0d1c4795`; session `sess-2af85a4de3edbc68af085ac593fd5470`; command `cmd-281a0603cb236cfa1dd33efc03c43589` | Native mailbox client read terminal revision 2, four events through cursor 4, complete output and closed teardown, then wrote the exact ACK. Lifecycle read reports `terminal_acknowledged`, `acknowledged: true`, zero actionable pairs/backlog. |
| Queued factory after Dev restart | `simple04-20261018-s9-queued-factory-postrestart-01`; job `job-60ae9bb96f17283d87f39c2116b675da`; session `sess-f7c980980c12385c1681e01682122cb3`; command `cmd-35b29cfa8cbfc8da9f70d9733ed8ff47` | Succeeded, exit 0, complete event history/output, closed teardown after factory Dev restart. |

Mailbox, timeout, explicit-limit and health checks used temporary task-local
tests under `src/internal/mailboxclient` or `src/internal/runnercli`; all were
removed. Commands used the Mac Go 1.27.1 binary and shared caches. The mailbox
test was `go test ./src/internal/mailboxclient -run
'^TestSIMPLE04DevMailboxRunReadAndAck$' -count=1 -v`. The target timeout and
explicit-limit probes were `TestSIMPLE04RequestedTimeoutProbe` and
`TestSIMPLE04ExplicitPermittedOutputLimitProbe` in `src/internal/runnercli`.
The post-restart worker health read used `TestSIMPLE04WorkerHealthPostRestart`.
These were live acceptance probes, not committed regressions or additional
source gates.

### Dev restart isolation and final selected-lane postflight

Each Dev stop/start ran serially through `runner-lane` after the preceding
probe reached terminal state. The stop guard proved zero retained active work
before each stop. Linux PRD stayed active at PID `149913`, sandbox PRD at
`717823`, and factory PRD at `50302`; each had `Result=success` before and
after its Dev restart. Dev units restarted successfully. Mac Dev LaunchAgent
PIDs changed from `50865`/`50912` to `61821`/`61872`; Mac PRD LaunchAgent PIDs
remained `48708`/`48627`.

After restart, Linux and sandbox direct mTLS health both returned HTTP 200,
`readiness=ready`, build revision `2204e18`, and zero active sessions/commands,
queued commands/intents, and mailbox backlog. Factory Dev remained active at
PID `1247375`; its queued post-restart probe succeeded. Mac Dev health returned
ready at `2204e18` with zero active slots, queued work and mailbox backlog.
The final Mac Dev local health report also showed zero uncertain/pending remote
intents and zero storage errors.

### S9 blocker snapshot before S10

The factory Dev HTTPS listener was not reachable at configured public endpoint
`145.241.255.66:8444` from the Mac; Runner received TCP `connection refused`
before HTTP and produced no accepted work. Its queued bridge on existing SSH
port 22 passed before and after restart, and the factory Dev systemd unit is
active. Exact external action: the authorized network/provider operator must
inspect the current ingress mapping and permit the existing factory Dev TCP
8444 route from the Mac's configured egress, or provide the approved existing
direct route. No firewall or provider change was made.

The first Mac PRD post-restart health read was ready at revision
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`, with one existing session slot,
zero commands/queues, and `storage_errors_total=1`. A later health read at
`2026-10-18T03:59:31Z` returned `readiness=not_ready` and
`sqlite_writes: database_write_failed`; the service PIDs remained unchanged,
the existing session slot remained 1, and `storage_errors_total` became 2.
The PRD `runner_health_probes` row last recorded a successful Mac-ingress
check at `03:57:22Z`. Read-only `PRAGMA quick_check` returned `ok`, the Mac
data volume had about 154 GiB free, and filtered PRD logs contained only the
earlier `storage_errors_total=1` warning at `03:39:46Z`; no fresh SQLite error
detail was available. No PRD restart, configuration change, mailbox action,
or manual database edit was made. The built-in health endpoint's timestamp
`CheckWritable` probe is the only PRD database operation in this postflight;
its first probe succeeded and the later one failed. Exact owner action: an
authorized PRD operator must investigate why the existing SQLite write probe
failed and provide a fresh ready postflight before any PRD maintenance or
rollout dependency that relies on PRD health. Do not restart or repair PRD
without a separate concrete approval.

SIMPLE-04 remained **IN PROGRESS** at the end of S9. The S10 closeout below
records the subsequent factory direct-ingress repair and successful PRD
readiness verification. No QAS service was started, no production binary or
configuration was changed, and no held ACK was touched during S9.

### S10 — factory Dev direct route and Mac PRD database-write diagnosis

#### Factory Dev route

Read-only SSH inspection used the configured explicit `factory-sandbox`
identity and verified host key. `runnerd-dev.service` was active and its
listener was correctly bound to `10.0.0.30:8444`; local TCP connected there.
The existing PRD listener remained on `10.0.0.30:8443`, PID `50302`. The
public `145.241.255.66:8444` attempt from Mac was rejected at the factory host:
the nftables-backed persistent iptables configuration had an exact source
allow for port 8443, then a final host-prohibited reject, but no 8444 allow;
the reject counter increased by one during the failed probe. This establishes
the host firewall as the cause; no provider change was needed.

Before changing the rule, the factory Dev database was inspected read-only:
three closed sessions/commands/jobs, zero unreleased reservations and no
intents. Only Dev TCP `8444` was opened, from the configured Mac egress
`81.95.203.65/32`, before the existing final reject:

```text
-s 81.95.203.65/32 -p tcp --dport 8444 -m conntrack --ctstate NEW \
  -m comment --comment "Remote Session Runner factory-sandbox Dev mTLS Mac source" -j ACCEPT
```

The rule was saved with `netfilter-persistent save`. Exact pre-change rules
were preserved at `/etc/iptables/rules.v4.before-simple04-20261018`. The
existing 8443 rules, QAS route, unrelated firewall entries and services were
not changed. The public TCP probe then succeeded, and a fresh authenticated
Runner request through endpoint `factory-sandbox-poc-dev` succeeded:

| Check | Evidence | Result |
| --- | --- | --- |
| Dev direct mTLS work | Key `simple04-20261018-s10-direct-factory-01`; job `job-61b9190dd88d4222370fb17704ec0482`; session `sess-ac5e6a3363ebeb259a1710e9348718ea`; command `cmd-e8be957a7ff618967a1945ec09ffccf5` | `printf "SIMPLE-04 direct factory acceptance\n"`; succeeded, exit 0, complete output, terminal sequence 4, teardown closed |
| Post-work Dev state | Read-only factory Dev SQLite query and systemd/listener inspection | Four closed sessions/commands/jobs, zero unreleased reservations, no intents; `runnerd-dev.service` and PRD remain active; PRD PID unchanged |

No PRD/QAS port or route was touched. One preliminary Runner attempt used a
nonexistent local profile name and failed during local profile selection before
network publication; it accepted no work. The successful attempt used the
configured `factory-sandbox-poc-dev` endpoint.

#### Mac PRD database-write failure

The inspection used actual unsuffixed Mac LaunchAgent processes and their
private health sockets. `runner-local` PID `48708` and `runner-locald` PID
`48627` both used the installed PRD configuration and shared its expected
SQLite database. The database, WAL and SHM were owned by
`tomasz.walczuk:staff`, mode `0600`; parent directories were mode `0700`; no
ACL or file flags were present. `PRAGMA quick_check` returned `ok`, journal mode
was `wal`, and the volume had about 154 GiB free. Both service processes held
the DB/WAL/SHM open read-write as the service account. No competing process
other than the two intended PRD components was found.

At `2026-10-18T07:18:39Z`, sequential GETs of the actual PRD API and locald
`/health/ready` endpoints both returned HTTP 200, `readiness=ready`, and
`sqlite_writes=ready` at installed build `77910358a149d6e8fcdf4694498ebdf6c1f07f58`.
The API retained one pre-existing session slot and had zero command slots,
queues, intents and backlog. Locald also had one session slot and zero active
commands, queues, intents and backlog. That slot belongs to retained work and
was preserved. Locald reported cumulative `storage_errors_total=496` and
`cleanup_failures_total=1`; the API reported `storage_errors_total=2`. These
cumulative counters and the earlier health failure remain visible historical
signals even though current write/readiness checks pass.

Installed code maps a failed `CheckWritable` probe to generic
`database_write_failed` and does not log the underlying SQLite/OS error. The
service logs and unified log search contain no raw cause for the earlier
failure. An out-of-process probe from the restricted tool shell returned
`SQLITE_READONLY`, but that shell was denied access by its sandbox; it was not
running as or inside the production service and is not evidence of a service
permission failure. The actual service write probes succeeded on fresh checks.
No ownership, permission, WAL/SHM or database change was indicated or applied.

No Mac PRD process was restarted, and its config, binaries, database, WAL/SHM,
mailbox, retained reservation and held ACKs were not changed. The transient
`database_write_failed` status is cleared at the live readiness boundary, but
its original raw SQLite error cannot be identified retrospectively from the
available logs. This distinction is retained in the evidence; the successful
service-owned write probes satisfy current PRD readiness without claiming a
specific historical root cause.

#### SIMPLE-04 closeout

The two S9 blockers are cleared for SIMPLE-04: factory direct authenticated
acceptance passes after the narrowly scoped host rule, and current Mac PRD
service readiness/write checks pass without a production change. Mac, Linux,
sandbox and factory Dev remain on their S9 candidate bytes from synchronized
source `2204e18ecd6eddc916ffa39e75ac905d3dbe2851`; no candidate rebuild or
restart was performed for the route change. PRD remains on its pre-existing
installed version and process identities. Historical failed probes, the
original retained work/reservation and held ACK are preserved. The cumulative
Mac locald error/cleanup counters and missing raw cause of the earlier
transient remain recorded as diagnostic limitations; they did not make the
fresh service readiness endpoints fail. SIMPLE-04 is PASS, and QAS work may
begin under SIMPLE-05 with the exact Dev-tested per-host bytes.

### S11 — fresh PRD readiness and database diagnosis

The owner requested a fresh PRD check before continuing QAS. Read-only GETs
through the existing PRD owner-only Unix sockets at
`2026-10-18T09:06:12.129124Z` (`runner-local`) and
`2026-10-18T09:06:15.134583Z` (`runner-locald`) both reported
`readiness=ready`, `sqlite_writes=ready`, and installed build
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`. The API and local executor each
still report the one retained session slot, with zero active commands, queued
commands, queued intents and mailbox backlog. The API's cumulative
`storage_errors_total` is 2; locald's is 584 and `cleanup_failures_total` is
1. No retained session, request, reservation, ACK, PRD service or configuration
was changed.

The read-only owner-shell diagnosis confirmed the database is in WAL mode and
`PRAGMA quick_check` returns `ok`. `local.db` is 80 MiB, its WAL is about 4.2
MiB and SHM is 32 KiB. The state directory is `0700`; the database, WAL and
SHM are `0600`, owned by `tomasz.walczuk:staff`. The service root and state
parent are owner-only, with no ACL or file flags recorded by the earlier S10
inspection. The data volume has about 153 GiB free. `ps` and `lsof` show the
expected PRD `runner-locald` PID `48627` and `runner-local` PID `48708` as the
only processes holding these files open, both as `tomasz.walczuk`; each has
read-write handles to the database/WAL/SHM. No competing process was found.

The latest service warnings still say only
`queue worker could not claim command ... reason=operation_failed` (last
observed at `2026-10-18 11:06:11` Europe/Warsaw). The installed code maps
unclassified claim errors to that sanitized reason and does not preserve the
underlying SQLite/OS error in its logs. The fresh service-owned write probes
pass, so permissions, WAL handling, disk capacity and current PRD readiness do
not indicate a supported corrective file operation. The original transient
`database_write_failed` cause remains unproven; this is not evidence that the
recurring claim warning has been fixed. No PRD restart is safe while the
retained session slot remains, and no PRD change is justified by the available
evidence. The stable PRD binaries, configuration, database, WAL/SHM, services,
mailbox, firewall and credentials remain unchanged.

### S12 — owner-supplied filesystem trace

The owner reran the bounded root `fs_usage` capture against the exact unsuffixed
PRD locald PID `48627`; the earlier `pgrep -x runner-locald` result had listed
all three lane processes and was correctly rejected. The completed capture
covered `2026-10-18 12:23:27`–`12:23:47` Europe/Warsaw. Its 33,003 lines showed
SQLite file-descriptor lock (`fcntl SETLK`) and read (`pread`) activity, with no
`Err#`, `EACCES`, `EPERM`, `ENOSPC`, or SQLite error text. The captured calls do
not expose a path for each file descriptor. A subsequent read-only `lsof` of
the still-running PID mapped FD 8 to `local.db-shm` and FD 17 to `local.db`; the
same snapshot showed only the expected PRD API PID `48708` and locald PID
`48627` holding the shared PRD DB/WAL/SHM. The owner trace ended before the next
logged queue-claim warnings at `12:24:35` and `12:26:12`, so it did not capture
or correlate with either failure.

At `2026-10-18T10:27:26.281806Z`, a fresh locald `/health/ready` request
returned HTTP 200, `readiness=ready`, `sqlite_writes=ready`, and the original
installed build. It retained one session slot and showed zero active commands,
queued commands/intents, and mailbox backlog; `storage_errors_total` was 628
and `cleanup_failures_total` was 1. This health route commits its designed
timestamp-only SQLite probe row. Read-only SQLite checks returned
`PRAGMA quick_check=ok`, `journal_mode=wal`, and no controlled-restart plan.
The state/DB/WAL/SHM owner and modes remained `tomasz.walczuk:staff`, `0700`
for directories and `0600` for the files, with about 150 GiB free on the data
volume. These checks rule out neither an intermittent transaction-specific
failure nor a non-SQLite claim error. The cumulative storage-error counter is
not correlated to the two warning timestamps.

No production service, configuration, database file, WAL/SHM, mailbox,
reservation, request, ACK, or retained work was changed. The filesystem trace
did not identify the underlying cause, and the recurring queue-claim warning
remains unresolved; no PRD corrective change or restart is justified yet.

### S13 — owner-supplied three-minute filesystem trace

The owner supplied a second root `fs_usage -t 180` capture against the exact
unsuffixed PRD locald PID `48627`. The filtered output contains successful
`WrData` calls to `local.db-wal` at `12:40:47`–`12:40:50` and `12:42:42`,
successful database-page writes at `12:40:48`, and a WAL open at `12:42:40`.
The accompanying log tail still shows generic queue-claim warnings at
`12:40:45` and `12:42:39`; the `12:42:39` warning is followed by ordinary
database/WAL activity, but the trace does not identify a transaction or
associate those writes with a particular claim. The captured search returned
no `Err#`, `EACCES`, `EPERM`, or `ENOSPC` matches. The transcript does not show
the exact capture start/end timestamps, so do not treat nearby successful
filesystem calls as proof that either claim succeeded or failed at SQLite.

This evidence rules against a general inability to write the PRD database at
those observed moments, but does not expose a transaction-level SQLite error or
explain the sanitized `operation_failed` warnings. No PRD service, database,
WAL/SHM, mailbox, reservation, request, ACK, or retained work was changed. The
raw queue-claim cause remains unresolved; no production corrective change or
restart is justified by this trace.
