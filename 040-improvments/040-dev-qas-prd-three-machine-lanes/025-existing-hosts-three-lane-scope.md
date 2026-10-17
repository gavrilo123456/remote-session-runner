# Active scope: three Runner lanes on the existing hosts

**Owner scope reset — 2026-10-17.** Run Dev, QAS and PRD in parallel on the
existing Mac and the existing `linux-host`, `sandbox-host` and
`factory-sandbox` workers. Use their current accounts, disks and filesystems.
This is the authoritative active profile. It supersedes conflicting scope,
architecture and prerequisites in 005/010 and the former 015/020 rollout.
The revised [plan](015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md)
and [handbook](020-dev-qas-prd-three-machine-lanes-execution-handbook.md)
define its execution and evidence order. This document changes no runtime.

## 1. Result and boundaries

Each worker runs three independent Runner services. Each Mac lane runs its
own copies of the existing control-plane components, including local/locald.
Dev changes freely within its own installation; QAS tests a selected candidate;
the current unsuffixed PRD installation continues serving its existing users.
PRD remains pinned to its installed stable binaries and configuration until a
separately selected, tested promotion. Editing or pulling the source checkout
does not replace an installed binary or restart any service.

This scope adds no host, VM, account, cloud volume, APFS volume, external disk,
physical backup device, dedicated SSH daemon, cgroup migration or required
reboot. It does not require the historical root release authority, signer,
cryptographic ledger, separate PKI-bootstrap stack or `:9443` management service.
The former A01 disposable-volume package and advanced F pressure tests are not
prerequisites. Preserve their code, documents and evidence as historical work;
do not delete them or mark unrun tests PASS. Reuse applicable ordinary code only
when it supports this profile without activating that infrastructure.

## 2. Complete lane separation

PRD has no suffix. Dev uses `-dev`; QAS uses `-qas` in shared namespaces.
Files inside an already separate lane root may retain normal leaf names.

| Resource | PRD | Dev | QAS |
| --- | --- | --- | --- |
| Mac root under `~/Library/Application Support/` | `RemoteSessionRunner` | `RemoteSessionRunner-dev` | `RemoteSessionRunner-qas` |
| Mac LaunchAgent label suffix | existing unsuffixed labels | `-dev` | `-qas` |
| Linux root under `/home/ubuntu/.local/share/` | `remote-session-runner` | `remote-session-runner-dev` | `remote-session-runner-qas` |
| Linux service | `runnerd.service` | `runnerd-dev.service` | `runnerd-qas.service` |
| Remote HTTPS port, on every worker | `8443` | `8444` | `8445` |
| Queued SSH listener | existing port `22` | same port `22` | same port `22` |

Every lane has separate installed component binaries, complete configuration,
SQLite databases and sidecars, private sockets, mailbox roots, workspaces,
temporary files, logs, locks and selected release metadata. Mac local/locald
labels and plist names receive the lane suffix. Other helpers use lane-rooted
executables and lane-specific process/service/log identities. No database,
socket or writable mailbox is shared or reached through another lane's alias.
PRD keeps every existing root, producer and route unchanged during Dev/QAS setup.

The installer derives destinations from the selected lane, refuses foreign
lane paths and port/label collisions, and acts only on that lane. Configuration
contains an explicit release lane for new Dev/QAS and newly promoted
configurations. The legacy unsuffixed PRD configuration remains compatible
and unchanged until a scheduled PRD upgrade. An execution-policy name such as
`linux-dev` does not select a release lane. Each Mac lane includes all three remote profiles;
each worker configuration identifies its own host and lane. A request resolves
only within the selected lane. Mac-local execution retains the existing
capability and policy when configured; remote-only execution is not a new
platform prerequisite. Dev/QAS inboxes are fresh and lane-specific; existing
external PRD inboxes are neither moved nor repointed.

## 3. Existing transports with explicit lane binding

Keep the existing direct mTLS model and authorization workflow. Dev/QAS use
distinct per-lane/per-worker client and server keys and explicit lane/profile
controller bindings. Validate endpoint, authenticated identity and configured
lane together; a foreign-lane credential or endpoint must fail before creating
work. Keep private material owner-only outside Git. Existing certificate
issuance can supply the new identities; a new root authority service is not
required. PRD trust and controller material stays unchanged during Dev/QAS setup.

Keep the existing SSH listener on port 22. Give each lane a distinct authorized
bridge key and a restricted fixed command bound to its installed bridge,
configuration and private socket. The authenticated key selects the lane;
request data cannot choose another lane, arbitrary binary or shell command.
Preserve the original PRD bridge entry and unrelated `authorized_keys` entries.
No new SSH daemon, SSH listener port or host-wide SSH restart is required.
New HTTPS routes use the existing host/network process with only the exact
approved 8444/8445 rules; verify actual reachability and authentication.

## 4. Small workloads on shared resources

Dev/QAS default to at most one running command and two sessions per lane per
host. Existing environment policy still bounds timeout, output and permitted
operations. Rotate each component's task logs at 16 MiB with four retained
files; account for all components in the lane's log budget. Never delete retained
responses, events, ACKs, active work or shared caches to satisfy a budget.

Use the standard shared Go caches required by AGENTS.md, not a cache copy per
lane or phase. All other runtime writes remain lane-specific. Check available
space and current host/PRD health before builds, tests and installation; refuse
new Dev/QAS work when the existing configured safety floor cannot be preserved.
Stop optional test load if PRD health degrades. This profile provides application
separation on shared CPU, RAM and storage, not dedicated resource reservations
or hostile-code containment between processes using the same account. It makes
no guarantee that arbitrary Dev jobs cannot exhaust shared host resources.

## 5. Build once, test, then promote the same bytes

Follow AGENTS.md: Mac source, Gitea primary, verified automatic GitHub mirror,
then clean fast-forwarded worker checkouts. Build each host's component set once
for a candidate. Record its Git SHA, platform, component paths and SHA-256
digests in a plain release manifest. Keep immutable installed candidate bytes
outside the source checkout. Promote that same host's exact component bytes
Dev → QAS → PRD; configurations remain separate. A rebuild is a new candidate
and repeats Dev/QAS checks. No cross-host source copying or new release server
is introduced. Keep the previous stable installed release for selected-lane
rollback. Initial Dev/QAS can use the current stable source without changing PRD.

Before replacing a selected lane, stop its new admissions and establish supported
terminal disposition for its accepted work. Preserve request identities, events,
ACK state and database authority; uncertain work stops that lane's change. Take
a consistent selected-lane configuration/database backup and record old binary
digests before replacement. Restart only its components and verify build identity,
routes, health and capacity release. Do not restart all lanes for convenience.
Rollback affects only that lane. Restore a database backup only while stopped
and when no later accepted work would be lost or replayed; otherwise preserve
current state and use compatible binaries or a forward repair. Never resubmit
retained requests or manually release capacity to make a rollout pass. Existing
PRD maintenance and exact retained-work guards remain binding for PRD changes.

## 6. Bounded functional acceptance

Use temporary directories and short controlled fixtures; each live probe has a
natural duration of at most one minute. Do not fill disks, quotas or memory.
Required checks cover namespace/configuration refusal, all three direct and
queued remote routes, configured Mac-local operation, timeout and permitted
explicit limits, managed-session create/run/read/cancel/close, truthful terminal
events, ACK of only newly owned test requests, and released capacity. Verify
foreign-lane authentication is rejected and starting/restarting Dev leaves QAS
and PRD identities, routing and ordinary health unchanged. Repeat candidate
acceptance on QAS and after an approved PRD promotion, using the exact same
per-host bytes. A failed capability blocks that candidate; repair within this
lane assignment and retest. Historical bug records and unrun advanced gates
remain truthful; they do not trigger a new standalone infrastructure campaign.
