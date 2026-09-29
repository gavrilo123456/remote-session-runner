# Configuration reference

All configuration and credentials live outside the Git checkout. The selected
PoC validates exact account names, service roots, environment names, profiles,
and endpoint values. Do not generalize these files by adding aliases or a third
environment unless the code and tests are changed together.

## Fixed values

| Setting | Value |
| --- | --- |
| Mac account | `tomasz.walczuk` |
| Ubuntu account | `ubuntu` |
| Mac config | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml` |
| Ubuntu config | `/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml` |
| Direct public URL | `https://129.151.232.40:8443` |
| Ubuntu HTTPS bind | `10.0.0.200:8443` |
| Direct endpoint profile | `linux-poc` |
| Direct TLS version | TLS 1.3 |
| Local profile | `mac-workstation` |
| Remote profile | `linux-host` |

## Service-root layout

### Mac

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/
├── bin/                 runner, runner-local, runner-locald
├── config/              mac.yaml
├── secrets/             CA, direct client cert/key, dispatcher key, known_hosts
├── run/                 local-api.sock, locald.sock
├── state/               local.db
├── mailbox/             inbox, outbox, events, acks
├── workspaces/          session working directories
├── tmp/scripts/         temporary submitted-script files
├── backups/             reserved state-backup location
└── logs/                local.stdout.log, local.stderr.log, locald.stdout.log, locald.stderr.log
```

### Ubuntu

```text
/home/ubuntu/.local/share/remote-session-runner/
├── bin/                 runnerd, entrypoint, optional SSH bridge/wrapper
├── config/              linux.yaml, client-principals.yaml, optional SSH map
├── secrets/             server certificate/key and trusted client CA
├── run/                 runnerd.sock
├── state/               remote.db
├── workspaces/          session working directories
├── tmp/scripts/         temporary submitted-script files
└── backups/             reserved state-backup location
```

The directories above must be owned by the selected account and mode `0700`.
Config and secret files must be regular owner-only files (mode `0600`), with no
symlinks. The daemon rejects unsafe permissions rather than weakening them.

## Service-manager files

| Host | Installed service definition | Runtime logs |
| --- | --- | --- |
| Mac | `$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist` and `com.remote-session-runner.locald.plist` | `<Mac service root>/logs/local*.log` and `<Mac service root>/logs/locald*.log` |
| Ubuntu | `/etc/systemd/system/runnerd.service` | `journalctl -u runnerd.service` |

## Mac configuration

The first Mac installer run copies
[`deploy/macos/mac.yaml.example`](../deploy/macos/mac.yaml.example) to the
selected config path and stops. Review it, provision the external secret files,
then rerun the installer.

The selected Mac file contains these important fields:

| Field | Why it matters |
| --- | --- |
| `mac.account`, `mac.service_root` | Pins the selected macOS account and root. |
| `api_socket`, `locald_socket`, `sqlite`, `mailbox_root` | Private local ingress, executor, state, and mailbox paths. |
| `workspaces`, `script_temp_root`, `backups` | Runtime working paths below the service root. |
| `remote_endpoint_profile`, `remote_endpoint` | Names `linux-poc` and the public direct HTTPS endpoint. |
| `remote_server_ca_certificate` | CA file that validates the Ubuntu server certificate. |
| `ssh_host_alias`, `ssh_known_hosts` | Pinned host identity for the queued remote route. |
| `direct_client_certificate`, `direct_client_private_key` | Direct mTLS identity. The private-key value is a file reference. |
| `dispatcher_ssh_key` | Private key reference for the restricted queued SSH bridge. |
| `reconciliation_deadline` | Maximum time before a queued remote mutation becomes indeterminate. |

The file must use `version: 1`. Its environment registry must retain the two
selected entries:

```yaml
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets:
      - kind: local
        profile: mac-workstation
    allowed_source_modes: [empty, local_worktree]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: local_user
        id: tomasz.walczuk
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets:
      - kind: remote
        profile: linux-host
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: queued_mac
        id: tomasz.walczuk
      - type: direct_mtls
        id: tomasz.walczuk
```

The supplied example is the source of truth for the complete selected Mac
configuration. Keep its exact absolute paths unless the implementation is
changed as well.

## Ubuntu configuration

Create the owner-only Linux config before installing `runnerd.service`:

```yaml
version: 1
linux:
  account: ubuntu
  service_root: /home/ubuntu/.local/share/remote-session-runner
  sqlite: /home/ubuntu/.local/share/remote-session-runner/state/remote.db
  private_socket: /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock
  workspaces: /home/ubuntu/.local/share/remote-session-runner/workspaces
  script_temp_root: /home/ubuntu/.local/share/remote-session-runner/tmp/scripts
  backups: /home/ubuntu/.local/share/remote-session-runner/backups
  direct_https_bind: 10.0.0.200:8443
  direct_public_endpoint: https://129.151.232.40:8443
  server_cert: /home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
  client_ca: /home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
  client_principal_map: /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
  tls_min_version: '1.3'
  runtime_adapter: linux-host-process
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets:
      - kind: local
        profile: mac-workstation
    allowed_source_modes: [empty, local_worktree]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: local_user
        id: tomasz.walczuk
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets:
      - kind: remote
        profile: linux-host
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: queued_mac
        id: tomasz.walczuk
      - type: direct_mtls
        id: tomasz.walczuk
```

The server private key remains in the external file named by
`linux_server_private_key`; never place its contents in this document, the
config, a commit, or command output.

### Direct-client principal map

`runnerd` accepts a direct client certificate only when its URI SAN maps to a
known controller. The selected map is
`/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml`:

```yaml
version: 1
principals:
  - uri_san: urn:remote-session-runner:controller:runner-tomasz-direct
    controller_type: direct_mtls
    controller_id: tomasz.walczuk
```

Provision a client certificate whose URI SAN exactly matches that value, signed
by `client-ca.pem`. The certificate and private key stay on the Mac under
`secrets/`; the server receives only the certificate during the TLS handshake.

### Queued SSH controller map

Queued remote execution is a different route from direct mTLS. When it is
needed, add an owner-only controller map under the Ubuntu config directory:

```yaml
version: 1
keys:
  'SHA256:<dispatcher-public-key-fingerprint-without-padding>':
    controller_type: queued_mac
    controller_id: tomasz.walczuk
```

The matching public key gets one `restrict,command=...` entry in Ubuntu
`authorized_keys`. Follow [the setup runbook](setup.md#optional-enable-the-queued-ssh-route)
and the corrected forced-command details in
[`deploy/ssh/README.md`](../deploy/ssh/README.md). Direct mTLS does not make
this queue bridge available.

## Service limits and retention

| Limit | Selected value |
| --- | ---: |
| Active sessions | 20 |
| Running commands | 4 |
| Serialized request | 1 MiB |
| Script bytes | 128 KiB UTF-8 |
| Command timeout | 30 minutes |
| Idle session timeout | 30 minutes |
| Maximum session lifetime | 4 hours |
| Output per command | 100 MiB |
| Subscriber buffer | 1 MiB |
| Persistence queue | 16 MiB |
| Metadata and idempotency retention | 90 days |
| Output retention | 30 days |
| Mailbox valid-ACK cleanup | 24 hours after ACK |
| Mailbox unacknowledged terminal cleanup | 7 days after publication |

These are service limits reported in session status. A successful request is
not evidence that an eventual command result will fit below every limit; always
inspect its terminal state and output flags.

## Validation and safe handling

- Configuration files reject unknown fields, aliases, wrong account names,
  insecure modes, symlinks, and paths outside the fixed service roots.
- The Mac config and secret references must be readable only by
  `tomasz.walczuk`; Ubuntu equivalents only by `ubuntu`.
- `runner-local doctor`, `runner-locald doctor`, and `runnerd doctor` validate
  their respective runtime configuration. See [operations](operations.md) for
  commands. A doctor probe writes a timestamp-only SQLite health record, so it
  is diagnostic rather than read-only.
- Restart a service after a configuration change. Do not edit an installed
  config while a deployment script is replacing service files without first
  following the relevant setup runbook.
