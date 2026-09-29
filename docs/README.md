# Remote Session Runner documentation

Remote Session Runner is a two-host proof of concept for running scripts from a
Mac on either the Mac itself or an Ubuntu host. The selected PoC has fixed
accounts, addresses, profiles, and service roots. It deliberately has no
Podman deployment, tunnel, browser terminal, or interactive PTY.

Start here:

| Document | Use it for |
| --- | --- |
| [Architecture](architecture.md) | Components, trust boundaries, execution routes, and supported scope. |
| [Configuration reference](configuration.md) | Exact selected paths, config fields, secret locations, permissions, and limits. |
| [Setup runbook](setup.md) | Provisioning or reinstalling the Mac and Ubuntu services. |
| [CLI user guide](user-guide.md) | Creating sessions, running scripts, replaying output, cancelling, and closing. |
| [Mailbox guide](mailbox.md) | Using the owner-only file mailbox instead of the CLI. |
| [API reference](api.md) | Direct mTLS and Mac Unix-socket API transport, endpoint inventory, and OpenAPI. |
| [Operations runbook](operations.md) | Health checks, logs, metrics, troubleshooting, and recovery limits. |

## Selected PoC at a glance

| Item | Selected value |
| --- | --- |
| Mac execution account | `tomasz.walczuk` |
| Ubuntu execution account | `ubuntu` |
| Mac service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu service root | `/home/ubuntu/.local/share/remote-session-runner` |
| Direct public endpoint | `https://129.151.232.40:8443` |
| Ubuntu listener bind | `10.0.0.200:8443` |
| Direct transport | TLS 1.3 with mandatory client certificates (mTLS) |
| Local target | `mac-dev` / `local` / `mac-workstation` |
| Remote target | `linux-dev` / `remote` / `linux-host` |

The direct mTLS route has been exercised against the Runner application. A
previous temporary TLS probe validated transport credentials only; it was not
an application test. The queued SSH route needs its separate restricted bridge
authorization and should be treated as unavailable until that host setup is
installed and verified.

## Read before operating

- A workspace is a starting directory, not a sandbox. Scripts retain the OS
  permissions of `tomasz.walczuk` on the Mac and `ubuntu` on Ubuntu.
- A session keeps a Bash process so shell state can persist between discrete
  `exec` calls. It is not an interactive shell or terminal emulator.
- Do not put configuration secrets, client keys, server keys, SQLite files, or
  service output in Git. They live below the service roots outside the checkout.
- A resource belongs to the controller that created it. Keep using the same
  ingress route for its status, events, cancellation, and close operation.
- The durability evidence covers software-crash recovery. Physical power-loss
  recovery has not been verified and is not a supported guarantee.
