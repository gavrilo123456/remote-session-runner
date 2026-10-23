# Remote Session Runner documentation

Remote Session Runner is a controlled Mac-to-Ubuntu execution proof of concept.
The installed policy can route work to the Mac or to named configured remote
profiles, while keeping selection explicit and auditable. It has no container
deployment, tunnel, browser terminal, interactive PTY, arbitrary host/account
selection, or automatic fallback.

## Start here

| Document | Use it for |
| --- | --- |
| [From a code change to production](promotion-guide.md) | Short, plain-language Dev → QAS → PRD guide: build once, test, approve, deploy and recover. |
| [Architecture](architecture.md) | Components, trust boundaries, multi-inbox routing, diagrams, and supported scope. |
| [Configuration reference](configuration.md) | Exact paths, V1 compatibility, V2 registries, active policy, secret handling, and limits. |
| [Setup and upgrade runbook](setup.md) | First install, V1→V2 upgrade, safe external mailbox roots, deliberate mailbox retirement, service deployment, bridge setup, and P157 onboarding. |
| [Existing-host lanes](lanes.md) | Select, inspect, update, and roll back the existing Mac and Ubuntu Dev/QAS lanes while preserving stable PRD. |
| [Factory-style release catalog design](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md) | Technical release layout, lane selectors, promotion, rollback and migration rules; consult phase evidence for installed status. |
| [CLI user guide](user-guide.md) | Local, direct remote, queued remote, sessions, events, cancellation, and close. |
| [LLM client guide](llm-client-guide.md) | Practical guide for LLMs and coding agents: local read/edit work, mailbox-only execution and validation, route selection, results, ACKs, retries, lifecycle status, and safe failure handling. |
| [Mailbox guide](mailbox.md) | Native and direct workspace file-mailbox integration, roots, defaults, overrides, deterministic request-result lookup, lifecycle status, events, ACKs, retries, and safe invalid-input diagnostics. |
| [API reference](api.md) | Direct mTLS and Unix-socket transports, shared v1 HTTP/JSON behavior, and the Mac-only lifecycle-status extension. |
| [Operations runbook](operations.md) | Health, queue and slot gauges, metrics, logs, read-only lifecycle triage, service refresh, diagnostic triage, and recovery limits. |
| [Mac continuity backup and restore](../deploy/backup/README.md) | Pre-wipe offline continuity archive, integrity verification, and post-install restore sequence. |
| [Current-host evidence](current-host-evidence.md) | What P155, the P157 host gates, P158, P159, and P166 accepted, what remains unaccepted, and the per-host boundary. |

## Current controlled configuration

| Item | Current value |
| --- | --- |
| Mac execution account | `tomasz.walczuk` |
| Ubuntu execution account | `ubuntu` |
| Mac service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu service root | `/home/ubuntu/.local/share/remote-session-runner` |
| Active mailbox roots | `default` at `mailbox/`; `analytics` at `mailboxes/analytics/`; `slidestud-io` at `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`; `agents-work-dispatcher` at `/Users/tomasz.walczuk/projects/agents-work-dispatcher/tmp/mailbox-`; `agentic-software-factory-runner-direct` at `/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-` |
| Accepted remote profiles | `linux-host`; `sandbox-host`; `factory-sandbox` |
| Current direct endpoint | `linux-poc` at `https://129.151.232.40:8443` |
| Current Ubuntu listener | `10.0.0.200:8443` |
| Sandbox direct endpoint | `sandbox-poc` at `https://132.226.205.205:8443` |
| Sandbox Ubuntu listener | `10.0.0.14:8443` |
| Factory sandbox host | `ubuntu@oracle-waclaw-dziwulski-ubuntu-micro-01` (Ubuntu 26.04, `x86_64`) |
| Factory sandbox direct endpoint | `factory-sandbox-poc` at `https://145.241.255.66:8443` |
| Factory sandbox Ubuntu listener | `10.0.0.30:8443` |
| Factory sandbox queued context | `ubuntu-factory-sandbox` = `factory-sandbox-dev` / `remote/factory-sandbox` |
| Direct transport | TLS 1.3 with mandatory client certificates |

P155 accepted the legacy `default` inbox with a local default and the
`analytics` inbox with an allowed queued override to the current `linux-host`.
P157 separately accepted `sandbox-host` (`sandbox.env` resolves to
`ubuntu@132.226.205.205`) after repair on source revision
`8873852ddc9ab33093c105371de93a3695d99b89`. Its queued context is
`ubuntu-sandbox` (`sandbox-dev` / `remote/sandbox-host`). `analytics` permits
it as an explicit complete override; the external `slidestud-io` inbox defaults
to it. P158 accepted the latter through its native marker-last exchange. P159
then accepted its direct workspace-file `0644` request/ACK path with private
`0600` response/event projections, complete events, and a sandbox zero-work
check. P165/P166 added and accepted the separate private `0600` malformed-
ingress diagnostic path; it does not create an accepted exchange or remote
work. `default` does not permit `ubuntu-sandbox`.

P157-factory-sandbox separately accepted `factory-sandbox` at
`ubuntu@oracle-waclaw-dziwulski-ubuntu-micro-01` for
`factory-sandbox-dev` / `remote/factory-sandbox`. Its public direct route is
`factory-sandbox-poc`, while `ubuntu-factory-sandbox` is its distinct queued
context. The external `agentic-software-factory-runner-direct` inbox is rooted at
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-`,
recognizes the `agentic-software-factory` repository alias, defaults to that
queued context, and permits `mac-local` as its only override. The direct mTLS,
queued bridge, and mailbox terminal-response/event/ACK gates are separate;
one does not establish another. Other physical machines remain **NOT RUN**
until their separate P157 onboarding and end-to-end evidence pass.

P157 records the original Factory mailbox root. The direct root is its
replacement; the planned controlled retirement and the still-separate
legacy-tree removal are recorded in
[`P167`](../040-implementation-evidence/P167-factory-mailbox-retirement.md).
Only a fully unwound fresh-residue fence may restore the prior services;
all other retirement activation errors remain no-rollback candidate repair.

BUG-015 later corrected the shared persistent-shell `errexit` boundary. A
fresh harmless `slidestud-io` sandbox request now proves that an ordinary
strict-shell nonzero result is terminal `failed` with complete output and
closed teardown. That is a corrective runtime result, not a new host gate or a
result for either historical Logger workflow.

Runner application readiness over direct mTLS was exercised for all three
accepted endpoints. A prior temporary TLS probe validated transport only. Software-
process-crash recovery is evidenced; physical power-loss recovery is
unverified.

## Read before operating

- A workspace is a starting directory, not a sandbox. Scripts retain the OS
  permissions of `tomasz.walczuk` or `ubuntu`.
- A session retains Bash state across discrete `exec` calls; it is not an
  interactive shell.
- Keep keys, certificates, SQLite files, logs, and output outside tracked source.
- Controller ownership and endpoint route are immutable for an existing
  resource. Continue status/events/cancel/close through the same route.
- A configuration entry means policy exists. A new physical host requires its
  own P157 proof before it is available.
