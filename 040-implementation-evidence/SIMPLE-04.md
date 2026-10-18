# SIMPLE-04 — Dev deployment and coexistence acceptance

**State: IN PROGRESS.** The S3 Dev candidate is installed and active on the Mac
and all three workers. Linux and sandbox direct one-off probes passed;
queued SSH pings passed on all three. The factory public direct route is
refused, and one queued one-off probe exposed a local command-event lookup bug
now fixed in source but not yet delivered or installed. Mailbox lifecycle,
managed-session, timeout/limit, Mac-local and complete route acceptance remain.
Existing PRD is still in service; QAS is not started.

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

## Current source gate and remaining acceptance

At the uncommitted S4 source contents, the focused regression passes; full Mac
`make check` exits 0 and `make build` exits 0. The fix and this evidence update
still require delivery through primary Gitea, exact GitHub mirror verification,
and clean worker fast-forwards, followed by worker checks and a rebuilt S4
candidate. After guarded Dev-only update, resume the exact queued job, verify
capacity release, then test all direct/queued profile operations, Mac-local
execution, managed-session lifecycle, timeout and permitted limits, and the
uniquely owned mailbox request/output/events/ACK lifecycle. Preserve every
historical response and held ACK. Do not mark SIMPLE-04 PASS until the factory
direct route and all remaining matrix checks pass.

PRD service/configuration/mailbox/credentials and host firewall rules were not
changed. The only SSH authority changes are the Dev-only forced bridge entries
and the Mac Dev known-host aliases; original worker SSH authority backups are
verified above. No held ACK was touched.
