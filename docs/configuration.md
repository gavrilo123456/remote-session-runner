# Configuration reference

All configuration, credentials, service state, output, and logs live outside
the Git checkout. Files are loaded only when they are regular, non-symlink,
owner-only files. The selected accounts are fixed in this PoC: Mac work runs as
`tomasz.walczuk`; every configured remote profile runs as `ubuntu`.

## Current controlled PoC

| Setting | Current value |
| --- | --- |
| Mac account and root | `tomasz.walczuk`; `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu account and root | `ubuntu`; `/home/ubuntu/.local/share/remote-session-runner` |
| Mac active config | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml` |
| Current remote profile | `linux-host` |
| Direct endpoint name and URL | `linux-poc`; `https://129.151.232.40:8443` |
| Ubuntu HTTPS bind | `10.0.0.200:8443` |
| Local target | `mac-dev` / `local` / `mac-workstation` |
| Current remote target | `linux-dev` / `remote` / `linux-host` |
| Direct transport | TLS 1.3 with mandatory mTLS |

The public URL is a client address. `10.0.0.200:8443` is only the current
Ubuntu listener bind. A future named host needs its own listener, endpoint or
bridge definition, credentials, and P157 acceptance. It must not reuse these
values as proof that it is ready.

## Service-root layout

### Mac — `tomasz.walczuk`

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/
├── bin/                 runner, runner-local, runner-locald
├── config/              mac.yaml; optional reviewed mac.next.yaml
├── secrets/             CA, direct client certificate/key, dispatcher key, known_hosts
├── run/                 local-api.sock, locald.sock
├── state/               local.db
├── mailbox/             default inbox, outbox, events, acks
├── mailboxes/
│   └── analytics/       analytics inbox, outbox, events, acks
├── workspaces/          session working directories
├── tmp/scripts/         temporary submitted-script files
├── backups/             reserved state-backup location
└── logs/                local*.log and locald*.log
```

The `default` root deliberately retains the legacy `mailbox/` path. Every
additional configured root is exactly `mailboxes/<inbox-id>/`; it is not a
free-form path.

### Ubuntu — `ubuntu`

```text
/home/ubuntu/.local/share/remote-session-runner/
├── bin/                 runnerd, entrypoint, optional bridge and wrapper
├── config/              linux.yaml, principal map, optional queued-SSH map/key/manifest
├── secrets/             server certificate/key and trusted client CA
├── run/                 runnerd.sock
├── state/               remote.db
├── workspaces/          session working directories
├── tmp/scripts/         temporary submitted-script files
└── backups/             reserved state-backup location
```

Service and mailbox directories are owned by the selected account and mode
`0700`. Config and secret files are regular owner-only files at mode `0600`.
The daemon rejects unsafe modes, ownership, symlinks, unknown fields, and paths
outside the selected roots.

## Mac configuration schemas

### Version 1: compatibility bootstrap

[`deploy/macos/mac.yaml.example`](../deploy/macos/mac.yaml.example) is the
first-install template. It remains a valid `version: 1` configuration and is
read as one implicit `default` inbox, `mac-local` context,
`ubuntu-current` context, `linux-host` remote profile, and `linux-poc` direct
endpoint. Its legacy scalar fields include `mailbox_root`,
`remote_endpoint_profile`, `remote_endpoint`, `ssh_host_alias`, and
`ssh_known_hosts`.

Use version 1 only for the initial compatibility installation. Do not add
multiple inboxes or another host by appending fields to it.

### Version 2: installed multi-inbox policy

The current Mac installation uses `version: 2`. Version 2 requires `mac`,
`environment_registry`, `execution_contexts`, and `mailboxes`; every configured
remote context also needs its matching `remote_hosts` entry. The active policy
uses these top-level sections:

| Section | Purpose |
| --- | --- |
| `mac` | Account, service root, private sockets, database, runtime paths, and reconciliation deadline. |
| `environment_registry` | Allowed environment, account, controller, target, source-mode, and repository-alias policy. |
| `execution_contexts` | Named immutable `{environment, execution_target}` pairs. |
| `remote_hosts` | Named `ubuntu` profiles and their pinned queued bridge and optional direct mTLS endpoint. |
| `mailboxes` | Named mailbox root, repository aliases, default context, and allowed contexts. |

Version 2 deliberately rejects the version-1 scalar mailbox, SSH, direct
endpoint, and top-level secret-reference fields. Its bridge, CA, certificate,
and private-key entries are owner-only file paths beneath `secrets/`, nested
under the matching `remote_hosts` entry. No credential contents belong in YAML
or Git.

Start from the complete checked-in template:

```text
deploy/macos/mac.v2.yaml.example
```

The template intentionally contains only the accepted `linux-host`. The active
P155 policy adds `analytics` outside Git, with the following effective policy:

```yaml
version: 2
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  ubuntu-current:
    environment: linux-dev
    execution_target: {kind: remote, profile: linux-host}

remote_hosts:
  linux-host:
    account: ubuntu
    queued_bridge:
      host: 129.151.232.40
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519"
    direct_endpoint:
      name: linux-poc
      url: https://129.151.232.40:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"

mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
  analytics:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics"
    repository_aliases: [analytics-dbt]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
```

Keep the complete `mac` and `environment_registry` sections from the template;
the abbreviated listing above only shows the multi-inbox policy. The
environment registry permits the local account for `mac-dev` and `ubuntu` for
`linux-dev`. The current remote source mode is `empty`.

### Version-2 validation rules

- `default` is required and its root is exactly
  `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox`.
- An extra inbox ID has root exactly
  `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/<inbox-id>`.
  Roots cannot duplicate, nest, or overlap.
- Each context pairs one configured environment with one allowed immutable
  target. Duplicate environment/target pairs are rejected.
- A mailbox default must be in its `allowed_execution` list. A remote context
  allowed in a mailbox needs a configured queued bridge; mailbox work never
  switches to direct mTLS.
- A direct endpoint name binds exactly one remote target profile. The CLI
  rejects an endpoint/target mismatch before a mutation.
- Inbox IDs, aliases, contexts, profile names, and endpoint names are bounded
  safe identifiers. `repository_aliases` are audit/policy labels only.
- The mailbox registry is append-only in this PoC. Adding a root is supported;
  removing, renaming, or relocating one requires a later explicit migration
  after its durable work and files are retired.

## Safe Mac V1 → V2 upgrade

1. **Mac — `tomasz.walczuk`:** copy and review the V2 template as the
   owner-only candidate
   `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml`.
2. Keep the active `config/mac.yaml` untouched. Provision only referenced
   owner-only secret files; never put their contents in the candidate.
3. Run the candidate installer described in the [setup runbook](setup.md#3-upgrade-to-version-2-or-add-an-inbox).
4. The installer validates while the old ingress is live, quiesces the Mac
   services, and makes a final read-only retained-mailbox check. For a
   schema-24 database, that check treats existing work as implicit `default`;
   it does not migrate or write the registry.
5. At the candidate-activation boundary, the new policy is registered and the
   database can migrate to schema 27. A version-1 binary cannot reopen that
   namespaced schema. Before the boundary the installer restores the V1
   services on failure. After it, it keeps the staged candidate for repair or
   re-run and does not revive V1.

The installer keeps the separately reviewed `mac.next.yaml` after a successful
activation; it stages a copy as the active `mac.yaml`. Do not overwrite active
`mac.yaml` with a shell copy.

## Current Ubuntu configuration

The current accepted `linux-host` uses the existing Linux configuration at:

```text
/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml
```

It pins `account: ubuntu`, its service root, `runnerd.sock`, the direct bind
`10.0.0.200:8443`, public endpoint `https://129.151.232.40:8443`, TLS 1.3,
server certificate, client CA, principal map, and host-process adapter. The
server private key is only the owner-only file named by
`secret_references.linux_server_private_key`; do not print or commit it.

The direct client principal map is:

```text
/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
```

It maps the verified direct certificate URI SAN to
`direct_mtls/tomasz.walczuk`. The queued path has separate permanent
bridge/controller files under the Ubuntu `config/` directory. Direct mTLS does
not make the bridge available.

A future host can use a `version: 2` Linux configuration, which requires its
own `linux.remote_target_profile`. Every registered environment on that host
must permit that remote profile, and the Linux document rejects the Mac-only
`execution_contexts`, `remote_hosts`, and `mailboxes` sections. It needs
independent state, listener/certificates, key pin or direct mTLS material, and
P157 evidence; it must not copy the accepted host's state database or secrets.

## Limits and retention

| Limit | Selected value |
| --- | ---: |
| Active sessions | 20 |
| Running commands | 4 |
| Serialized request | 1 MiB |
| Script bytes | 128 KiB UTF-8 |
| Command and idle timeout | 30 minutes |
| Maximum session lifetime | 4 hours |
| Output per command | 100 MiB |
| Subscriber buffer | 1 MiB |
| Persistence queue | 16 MiB |
| Metadata and idempotency retention | 90 days |
| Output retention | 30 days |
| Valid-ACK cleanup | 24 hours after ACK |
| Unacknowledged terminal cleanup | 7 days after publication |

`runner-local doctor`, `runner-locald doctor`, and `runnerd doctor` validate
their respective runtime configuration. A doctor command writes a timestamp
health record to SQLite, so it is diagnostic rather than read-only. Use the
[operations runbook](operations.md) for command examples.
