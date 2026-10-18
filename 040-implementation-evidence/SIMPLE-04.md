# SIMPLE-04 — Dev deployment and coexistence acceptance

**State: IN PROGRESS.** S6 source revision `2b50bf761e60bad9e49353ef9afc08e6de6e94b2`
and evidence checkpoint `6c4aa9ef73493ac513ef6897c6f414d7f7dc7b3e` are
delivered and synchronized. S6 candidates are installed and ready on the Mac
and all three workers. A fresh correctly configured Mac-local probe still
fails during session startup; no command was created or run. The bounded
queueworker diagnostic and integration regression are delivered and pass the
Mac and all three worker `make check build` gates. Diagnostic candidates and a
fresh Mac-local probe remain pending. PRD remains installed and running; QAS
is not started.

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

The current source change logs only bounded Mac-local empty-source runtime
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
rebuild and selected-Dev refresh remain the next operation.

Rebuild the four host candidates from the exact new revision and refresh only
Dev through the guarded installer. Then complete the
remaining acceptance: fresh managed cancellation, factory direct ingress,
configured Mac-local execution, requested timeout, permitted explicit limits,
all required direct/queued profile routes, owned mailbox request/output/event/
ACK lifecycle, Dev restart isolation and fresh zero-work postflight. No held ACK
was touched. Do not mark SIMPLE-04 PASS until all required gates pass.

PRD service/configuration/mailbox/credentials and host firewall rules remain
unchanged. Existing SSH authority changes are limited to the Dev forced bridge
entries and Mac Dev known-host aliases; the original worker SSH authority
backups remain verified above.
