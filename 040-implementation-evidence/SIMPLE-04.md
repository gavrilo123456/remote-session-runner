# SIMPLE-04 — Dev deployment and coexistence acceptance

**State: IN PROGRESS.** The selected Mac Dev lane is installed and active. The
first worker Dev candidates/configurations are installed, but the lane start
and clean-stop checks exposed two additional defects corrected in the current
source changes. Worker Dev services are stopped pending the corrected
candidate. Factory Dev is installed but has not been started. No route, command,
mailbox, or live acceptance PASS is claimed. Existing PRD remains in service.

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
The S2 source changes in this evidence revision are not installed; S1 worker
services remain stopped for safe replacement.

## Current route findings and next gates

Fresh read-only inspection shows INPUT policy `ACCEPT` on all three workers,
including factory-sandbox; the earlier finding that factory INPUT ended in
`REJECT` is stale and withdrawn. No host firewall rule was changed or is
currently indicated. Public Dev 8444 reachability has not yet been tested from
the Mac. OCI CLI and `~/.oci/config` were absent on all three workers, so
provider ingress configuration cannot be inspected through those hosts. Test
the existing route first; only a measured failure that requires a cloud rule
will need an operator with provider access and an exact scoped rule request.
Queued SSH uses the existing port 22 and was installed without changing sshd.

Current next gates:

- Deliver the S2 source/evidence through Mac `dev` → Gitea → exact GitHub
  mirror → clean worker fast-forwards, then run worker `make check`.
- Rebuild S2 candidate bundles once; then perform guarded selected-Dev updates
  and start/stop checks. Preserve the S1 failure evidence above.
- Test direct mTLS and queued SSH routes, Mac-local health, bounded Dev
  command/managed-session behavior, truthful terminal/event/ACK evidence,
  capacity release, and before/after PRD identity/health checks.
- Do not claim SIMPLE-04 PASS until every required route and lifecycle gate
  passes.

PRD service/configuration/mailbox/credentials and host firewall rules were not
changed. The only SSH authority changes are the Dev-only forced bridge entries,
with exact original backups verified above. No held ACK was touched.
