# SIMPLE-04 — Dev deployment and coexistence acceptance

**State: IN PROGRESS.** The Mac Dev first-start exposed a lane-selection defect;
the selected Dev agents are stopped while the correction is delivered. No live
acceptance PASS is claimed. Linux Dev services have not yet been installed or
started. Existing PRD remains in service.

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

The source correction now loads the selected config before validating/creating
its root, accepts only the three canonical Mac lane roots, and derives the
mailbox fallback from the selected config's default mailbox. The legacy
V1/V2/PRD fallback remains the unsuffixed root. Focused regressions cover the
canonical-root allowlist and per-lane default-mailbox baseline. The focused
tests and Mac `make check` passed; the corrected source delivery and live retry
are still pending. This correction is within SIMPLE-04; it is not a separate
baseline bug campaign.

The unsuffixed Mac PRD API and locald were ready immediately before the Dev
start at revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`, with one existing
session, no active commands/queues, and no mailbox backlog. A fresh post-retry
comparison will be recorded before claiming coexistence.

## Exact remaining network gate

`linux-host` and `sandbox-host` have host INPUT policy `ACCEPT`, so their Dev
8444 route still requires a public reachability check after listener startup.
`factory-sandbox` has a final INPUT `REJECT` and currently admits only SSH plus
PRD 8443 from `81.95.203.65/32` and `31.61.247.138/32`. Thus its host firewall
will block the required Dev direct route from the Mac (`81.95.203.65/32`) on
TCP 8444.

The exact host rule required for factory Dev, if the owner approves, is to
insert this single allow before the final INPUT reject:

```sh
sudo iptables -I INPUT 5 -s 81.95.203.65/32 -p tcp -m tcp --dport 8444 -m conntrack --ctstate NEW -m comment --comment "Remote Session Runner factory-sandbox Dev mTLS Mac source" -j ACCEPT
```

The current host rules were read only; this rule has **not** been added. The
provider ingress rule set has not yet been established. A successful host rule
would not by itself prove the public route. Queued SSH remains on the existing
port 22 and does not require a firewall change.

## Current state and next gates

- Mac Dev E candidate/config/credentials and lane bridge host-key pins exist;
  both Dev agents are stopped after the zero-work failure diagnosis.
- No Ubuntu Dev candidate or service has been installed/started yet.
- No PRD files, services, credentials, mailboxes, or firewall rules were
  changed. No retained work or held ACK was touched.
- Source correction S must be delivered Mac → Gitea → exact GitHub mirror →
  clean worker fast-forwards. Then rebuild immutable host candidates from S,
  update the stopped Mac Dev lane with a verified zero-work backup, retry its
  health checks, and install/start the three Ubuntu Dev services.
- Continue direct and queued route checks on all three profiles, harmless
  command/output, bounded timeout/limit behavior, managed-session lifecycle,
  terminal/event/ACK and zero-work proof, Mac-local execution, and before/after
  PRD health. A firewall or provider rule remains an operation-specific gate;
  do not call overall Dev acceptance PASS until the direct route has actually
  passed.
