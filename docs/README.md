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
| [Current-host evidence](current-host-evidence.md) | What P155 accepted, what it did not accept, and the P157 per-host boundary. |

## Current controlled configuration

| Item | Current value |
| --- | --- |
| Mac execution account | `tomasz.walczuk` |
| Ubuntu execution account | `ubuntu` |
| Mac service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu service root | `/home/ubuntu/.local/share/remote-session-runner` |
| Active mailbox roots | `default` at `mailbox/`; `analytics` at `mailboxes/analytics/` |
| Current remote profile | `linux-host` |
| Direct endpoint | `linux-poc` at `https://129.151.232.40:8443` |
| Ubuntu listener | `10.0.0.200:8443` |
| Direct transport | TLS 1.3 with mandatory client certificates |

P155 accepted the legacy `default` inbox with a local default and the
`analytics` inbox with an allowed queued override to the current `linux-host`.
It did not accept another physical machine. The user-supplied `sandbox.env`
candidate (`ubuntu@132.226.205.205`) is **NOT RUN** until its separate P157
onboarding and end-to-end evidence pass.

The direct mTLS route was exercised against the Runner application. A prior
temporary TLS probe validated transport only. Software-process-crash recovery
is evidenced; physical power-loss recovery is unverified.

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
