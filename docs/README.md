# Remote Session Runner documentation

Remote Session Runner is a controlled Mac-to-Ubuntu execution proof of concept.
The installed policy can route work to the Mac or to named configured remote
profiles, while keeping selection explicit and auditable. It has no container
deployment, tunnel, browser terminal, interactive PTY, arbitrary host/account
selection, or automatic fallback.

## Start here

| Document | Use it for |
| --- | --- |
| [Architecture](architecture.md) | Components, trust boundaries, multi-inbox routing, diagrams, and supported scope. |
| [Configuration reference](configuration.md) | Exact paths, V1 compatibility, V2 registries, active policy, secret handling, and limits. |
| [Setup and upgrade runbook](setup.md) | First install, V1→V2 upgrade, service deployment, bridge setup, and P157 onboarding. |
| [CLI user guide](user-guide.md) | Local, direct remote, queued remote, sessions, events, cancellation, and close. |
| [Mailbox guide](mailbox.md) | Native safe file-mailbox integration, roots, defaults, overrides, events, ACKs, and retries. |
| [API reference](api.md) | Direct mTLS and Unix-socket transports plus v1 HTTP/JSON behavior. |
| [Operations runbook](operations.md) | Health, metrics, logs, service refresh, troubleshooting, and recovery limits. |
| [Current-host evidence](current-host-evidence.md) | What P155 and P157 accepted, what remains unaccepted, and the per-host boundary. |

## Current controlled configuration

| Item | Current value |
| --- | --- |
| Mac execution account | `tomasz.walczuk` |
| Ubuntu execution account | `ubuntu` |
| Mac service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu service root | `/home/ubuntu/.local/share/remote-session-runner` |
| Active mailbox roots | `default` at `mailbox/`; `analytics` at `mailboxes/analytics/` |
| Accepted remote profiles | `linux-host`; `sandbox-host` |
| Current direct endpoint | `linux-poc` at `https://129.151.232.40:8443` |
| Current Ubuntu listener | `10.0.0.200:8443` |
| Sandbox direct endpoint | `sandbox-poc` at `https://132.226.205.205:8443` |
| Sandbox Ubuntu listener | `10.0.0.14:8443` |
| Direct transport | TLS 1.3 with mandatory client certificates |

P155 accepted the legacy `default` inbox with a local default and the
`analytics` inbox with an allowed queued override to the current `linux-host`.
P157 separately accepted `sandbox-host` (`sandbox.env` resolves to
`ubuntu@132.226.205.205`) after repair on source revision
`8873852ddc9ab33093c105371de93a3695d99b89`. Its queued context is
`ubuntu-sandbox` (`sandbox-dev` / `remote/sandbox-host`), which is permitted
only by `analytics`; `default` does not permit it. Other physical machines
remain **NOT RUN** until their separate P157 onboarding and end-to-end evidence
pass.

Runner application readiness over direct mTLS was exercised for both accepted
endpoints. A prior temporary TLS probe validated transport only. Software-
process-crash recovery is evidenced; physical power-loss recovery is
unverified.

## Read before operating

- A workspace is a starting directory, not a sandbox. Scripts retain the OS
  permissions of `tomasz.walczuk` or `ubuntu`.
- A session retains Bash state across discrete `exec` calls; it is not an
  interactive shell.
- Keep keys, certificates, SQLite files, logs, and output outside Git.
- Controller ownership and endpoint route are immutable for an existing
  resource. Continue status/events/cancel/close through the same route.
- A configuration entry means policy exists. A new physical host requires its
  own P157 proof before it is available.
