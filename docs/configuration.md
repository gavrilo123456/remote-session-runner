# Configuration reference

All configuration, credentials, service state, output, and logs live outside
tracked source. An external mailbox can live below another local checkout only
in a runtime directory excluded from that checkout's VCS view. Files are loaded
only when they are regular, non-symlink, owner-only files. The selected accounts
are fixed in this PoC: Mac work runs as `tomasz.walczuk`; every configured remote
profile runs as `ubuntu`.

## Current controlled PoC

| Setting | Current value |
| --- | --- |
| Mac account and root | `tomasz.walczuk`; `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` |
| Ubuntu account and root | `ubuntu`; `/home/ubuntu/.local/share/remote-session-runner` |
| Mac active config | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml` |
| Active mailbox roots | `default` service-root mailbox; `analytics` service-root mailbox; `slidestud-io` at `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`; `agents-work-dispatcher` at `/Users/tomasz.walczuk/projects/agents-work-dispatcher/tmp/mailbox-`; `agentic-software-factory-runner-direct` at `/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-` |
| Accepted remote profiles | `linux-host`; `sandbox-host`; `factory-sandbox` |
| `linux-host` direct endpoint and bind | `linux-poc`; `https://129.151.232.40:8443`; `10.0.0.200:8443` |
| `sandbox-host` direct endpoint and bind | `sandbox-poc`; `https://132.226.205.205:8443`; `10.0.0.14:8443` |
| `factory-sandbox` direct endpoint and bind | `factory-sandbox-poc`; `https://145.241.255.66:8443`; `10.0.0.30:8443` |
| Local target | `mac-dev` / `local` / `mac-workstation` |
| Remote contexts | `ubuntu-current`: `linux-dev` / `remote` / `linux-host`; `ubuntu-sandbox`: `sandbox-dev` / `remote` / `sandbox-host`; `ubuntu-factory-sandbox`: `factory-sandbox-dev` / `remote` / `factory-sandbox` |
| Direct transport | TLS 1.3 with mandatory mTLS |

Each public URL is a client address, and each private bind belongs only to its
named Ubuntu host. The separately accepted `sandbox-host` is an ARM64 Ubuntu
host; `factory-sandbox` is an x86_64 Ubuntu 26.04 LTS host. A later named host
needs its own listener, endpoint or bridge definition, credentials, and P157
acceptance. It must not reuse any accepted profile's values as proof that it is
ready.

## Service-root layout

### Mac — `tomasz.walczuk`

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/
├── bin/                 runner, runner-local, runner-locald
├── config/              mac.yaml; optional reviewed mac.next.yaml
├── secrets/             CA, per-profile direct client certificate/key, dispatcher keys, known_hosts
├── run/                 local-api.sock, locald.sock
├── state/               local.db
├── mailbox/             default inbox, outbox, events, acks, diagnostics
├── mailboxes/
│   └── analytics/       analytics inbox, outbox, events, acks, diagnostics
├── workspaces/          session working directories
├── tmp/scripts/         temporary submitted-script files
├── backups/             reserved state-backup location
├── toolchains/          selected Go toolchain used to rebuild installed binaries
└── logs/                local*.log and locald*.log
```

The `default` root deliberately retains the legacy `mailbox/` path. A
non-default root is either exactly `mailboxes/<inbox-id>/` under the service
root or a clean absolute path outside it. An external root can be placed in a
repository-local ignored runtime directory when that is useful to its file-only
producer; Runner does not change that repository or any external ancestor.

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

Service-root directories and every configured mailbox root/child are owned by
the selected account at mode `0700`. Config and secret files are regular
owner-only files at mode `0600`. Mailbox ingress JSON and `.ready` files may be
exact `0600` from the native publisher or exact `0644` from a direct workspace
publisher. Runner-generated outbox, event, and diagnostic files remain `0600`.
For an external mailbox, every pre-existing ancestor must be a real directory
without group or other write access, and its immediate parent must belong to
the selected Mac user. Runner creates or verifies only the external root and
its `inbox`, `outbox`, `events`, `acks`, and `diagnostics` children. It rejects
unsafe modes, ownership, symlinks, unknown fields, and ambiguous paths.

## Mac configuration schemas

### Version 1: compatibility bootstrap

[`deploy/macos/mac.yaml.example`](../deploy/macos/mac.yaml.example) is the
first-install template. It remains a valid `version: 1` configuration and is
read as one implicit `default` inbox, `mac-local` context,
`ubuntu-current` context, `linux-host` remote profile, and `linux-poc` direct
endpoint. It cannot express the accepted `sandbox-host` profile. Its legacy
scalar fields include `mailbox_root`,
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

The checked-in template intentionally contains only `linux-host`, so it remains
a safe first V2 policy without sandbox credentials. The active current V2
policy is owner-only and outside Git. P155/P157/P158 established its
multi-inbox/two-host topology; P159 added direct workspace ingress and P165/P166
added malformed-ingress diagnostics. The separate P157 `factory-sandbox`
onboarding added a third accepted remote profile and its original external
mailbox. The direct-root replacement is recorded separately in
[`P167`](../040-implementation-evidence/P167-factory-mailbox-retirement.md).
The policy includes `analytics`, the
accepted `sandbox-host` with its explicitly allowed `ubuntu-sandbox` override,
and the external `slidestud-io` inbox that defaults to that context:

```yaml
version: 2
environment_registry:
  linux-dev:
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
  sandbox-dev:
    base_system: Ubuntu 22.04.5 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: sandbox-host}]
  factory-sandbox-dev:
    base_system: Ubuntu 26.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: factory-sandbox}]
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  ubuntu-current:
    environment: linux-dev
    execution_target: {kind: remote, profile: linux-host}
  ubuntu-sandbox:
    environment: sandbox-dev
    execution_target: {kind: remote, profile: sandbox-host}
  ubuntu-factory-sandbox:
    environment: factory-sandbox-dev
    execution_target: {kind: remote, profile: factory-sandbox}

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
  sandbox-host:
    account: ubuntu
    queued_bridge:
      host: 132.226.205.205
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox_dispatcher_ed25519"
    direct_endpoint:
      name: sandbox-poc
      url: https://132.226.205.205:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox-direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox-direct-client.key"
  factory-sandbox:
    account: ubuntu
    queued_bridge:
      host: 145.241.255.66
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/factory-sandbox_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/factory-sandbox_dispatcher_ed25519"
    direct_endpoint:
      name: factory-sandbox-poc
      url: https://145.241.255.66:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/factory-sandbox-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/factory-sandbox-direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/factory-sandbox-direct-client.key"

mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
    durable_orphan_cleanup: false
  analytics:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics"
    repository_aliases: [analytics-dbt]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current, ubuntu-sandbox]
  slidestud-io:
    root: "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-"
    repository_aliases: [slidestud-io]
    default_execution: ubuntu-sandbox
    allowed_execution: [ubuntu-sandbox, mac-local, ubuntu-current]
  agents-work-dispatcher:
    root: "/Users/tomasz.walczuk/projects/agents-work-dispatcher/tmp/mailbox-"
    repository_aliases: [agents-work-dispatcher]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current, mac-local]
    durable_orphan_cleanup: false
  agentic-software-factory-runner-direct:
    root: "/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-"
    repository_aliases: [agentic-software-factory]
    default_execution: ubuntu-factory-sandbox
    allowed_execution: [ubuntu-factory-sandbox, mac-local]
    durable_orphan_cleanup: false
```

Use complete `mac` and `environment_registry` definitions in the owner-only
candidate; the abbreviated listing above omits their remaining required fields.
The environment registry permits the local account for `mac-dev` and `ubuntu`
for `linux-dev`, `sandbox-dev`, and `factory-sandbox-dev`. The current remote
source mode is `empty`. `default` retains only `mac-local` and
`ubuntu-current`; `analytics` may explicitly select `ubuntu-sandbox`;
`slidestud-io` uses `ubuntu-sandbox` as its default and also allows the other
two contexts; `agents-work-dispatcher` defaults to `ubuntu-current` while
allowing a Mac-local override; and `agentic-software-factory-runner-direct` defaults
to `ubuntu-factory-sandbox` while allowing only its factory context or an
explicit Mac-local override.

`durable_orphan_cleanup` is optional and defaults to `false` for every inbox.
It is a per-inbox owner decision after lifecycle-status review, not a retention
setting or a way to replay work. When explicitly `true`, the Mac's bounded
reconciliation pass can remove only a safe marker-only artifact tied to
durable proof that it cannot execute. It never removes JSON drafts, outbox
responses, event files, diagnostics, durable records, or remote work. Activate
the policy through a reviewed V2 candidate and refresh the Mac service; do not
edit the active file in place.

### Version-2 validation rules

- `default` is required and its root is exactly
  `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox`.
- An extra inbox ID has either the exact service-root form
  `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/<inbox-id>`
  or a clean absolute root outside the service root. External ancestors must
  already exist and be safe; the root and its five mailbox children are
  owner-owned `0700`. Roots cannot duplicate, nest, or overlap after
  conservative case and Unicode normalization.
- Each context pairs one configured environment with one allowed immutable
  target. Duplicate environment/target pairs are rejected.
- A mailbox default must be in its `allowed_execution` list. A remote context
  allowed in a mailbox needs a configured queued bridge; mailbox work never
  switches to direct mTLS.
- A direct endpoint name binds exactly one remote target profile. The CLI
  rejects an endpoint/target mismatch before a mutation.
- Inbox IDs, aliases, contexts, profile names, and endpoint names are bounded
  safe identifiers. `repository_aliases` are audit/policy labels only.
- The enrollment registry preserves every original ID/root pair append-only.
  Adding a root is supported. Renaming or relocating an existing pair is not:
  add a distinct replacement, then use the explicit retirement procedure below
  to omit the old pair. A retirement is an immutable audit record, and its ID
  and root cannot be reused in a later candidate.

### Deliberate external-mailbox retirement

Retirement is a controlled policy transition, not a YAML omission or a file
cleanup shortcut. First add and accept a distinct replacement ID/root in the
reviewed candidate. Then omit the superseded ID from that same complete
candidate and supply that one exact ID with `--retire-mailbox`. The candidate must not
rename the old ID, move it to a different root, reuse either value, or still
contain the ID being retired.

`runner-local validate-config --check-retained-mailboxes --retire-mailbox ID`
models this transition without writing the database. It rejects an unknown or
mismatched registered root, retained accepted work, or terminal work that
still requires an ACK. An exact repeat for the same omitted ID/root is
idempotent, which permits post-boundary repair; a candidate that reuses a
retired ID or root is rejected. Run
`--check-mailbox-directories` as well to verify the replacement candidate's
paths. Before the final installer invocation, stop every publisher that can
write to the legacy root: native integrations, direct-workspace publishers,
interactive clients, and automation. Keep them stopped through the handoff.
Quiescing Runner's services does not stop an independent file writer.

The installer repeats the retained-state check after it has quiesced its own
services. At the retirement activation boundary, it takes the legacy root's
exclusive owner-only `.runner-mailbox-ingress.lock`, creates and syncs the
owner-only `0600` `.runner-mailbox-retired` sentinel, then uses `fchmod` to
freeze both `inbox/` and `acks/` at `0500`. It scans both frozen directories
and refuses retirement if either has an entry. The exclusive lock remains held
while it revalidates durable state and records the immutable retirement and the
complete candidate set; a plain `validate-config` invocation does not retire
anything.

This is a cooperative ingress fence. The native `mailboxclient` takes its
shared lock and checks the persistent sentinel immediately before it publishes
a marker-last request or ACK pair. A raw or direct-workspace writer does not
participate. A same-UID process can deliberately evade this coordination, and
a process that retained a directory descriptor before the fence may also evade
the ordinary pathname check. Stop those writers rather than treating the lock
or modes as adversarial isolation.

There is one narrow pre-boundary refusal. If a **fresh** fence finds an entry
in the frozen ingress directories before the post-fence authority open,
migration, or retirement tombstone, Runner restores both directories to `0700`,
syncs them, removes and syncs the sentinel, and releases the exclusive lock.
Only after that complete, lock-held atomic unwind does it return the distinct
fresh-residue outcome, allowing the installer to restore the prior active
configuration and LaunchAgents. No retirement was recorded; inspect and
settle the residue, then repeat the read-only preflight.

Every other activation failure is no-rollback candidate repair. That includes a
preexisting fence, any incomplete fresh-fence unwind, fence setup failure, and
every failure after the post-fence authority open, migration, or tombstone,
including later durable-state, registration, path-preparation, or startup
failure. Outside the fully completed fresh-residue unwind, do not restore the
old configuration or restart its publishers, remove the sentinel, or restore
the ingress write modes; rerun the same retirement procedure from the candidate
state.

After a successful retirement, the prior configuration is deliberately
rejected and the retired ID/root cannot become active again. The durable
history remains in `local.db`. Runner does not delete an external mailbox tree
as part of retirement: inspect the new active configuration and its postflight
first, preserve anything required by retention policy, then make any exact
physical-tree removal a separate explicitly authorized operator action. Do not
remove a parent directory, edit SQLite, or remove registry/tombstone records.
Use the command sequence in the [setup runbook](setup.md#retire-a-superseded-external-mailbox).

## Safe Mac V1 → V2 upgrade

1. **Mac — `tomasz.walczuk`:** copy and review the V2 template as the
   owner-only candidate
   `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml`.
2. Keep the active `config/mac.yaml` untouched. Provision only referenced
   owner-only secret files; never put their contents in the candidate.
3. Run the candidate installer described in the [setup runbook](setup.md#3-upgrade-to-version-2-or-add-an-inbox).
4. The installer performs a descriptor-based, non-mutating mailbox-path
   preflight while old ingress is live. After it quiesces `runner-local` and
   before executor bootout, the staged `runner-locald preflight-restart` command
   opens the authority selected by the **active** `mac.yaml` in SQLite read-only
   mode; it does not use the candidate database setting. The guard runs even
   when launchd does not report a loaded locald. It permits the restart
   only when active session slots, active command slots, queued commands, and
   resumable one-off jobs in `creating_session`, `accepting_command`,
   `awaiting_command`, or `closing_session` are all zero. The guard accepts the
   supported schema-24 or current schema without migration. A missing,
   unreadable, unsupported, or ambiguous database fails closed, except for a
   true first local-executor install with no old locald, binary, LaunchAgent
   plist, socket, database, or SQLite sidecar. The check performs no health
   write, recovery, cleanup, migration, or registry write. It then makes the
   final read-only retained-mailbox check. For a schema-24 database, that check
   treats existing work as implicit `default`; it does not migrate or write the
   registry.
5. At the candidate-activation boundary, it records the complete candidate
   mailbox set durably before creating a missing external tree. It then creates
   or verifies only each root and its five children, hands off `mac.yaml`, and
   starts the services. The database can migrate to the current schema; a version-1
   binary cannot reopen that namespaced schema. Before the boundary the
   installer restores the V1 services on failure. After it, a filesystem failure
   retains the staged candidate for repair or re-run and does not revive V1.

The installer keeps the separately reviewed `mac.next.yaml` after a successful
activation; it stages a copy as the active `mac.yaml`. Do not overwrite active
`mac.yaml` with a shell copy.

## Accepted Ubuntu configurations

Each physical Ubuntu host holds its own owner-only configuration under the same
path on that host:

```text
/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml
```

| Profile | Host | Direct bind | Public endpoint | Evidence |
| --- | --- | --- | --- | --- |
| `linux-host` | `oracle-yuta-konopka-ubuntu-micro-02` | `10.0.0.200:8443` | `https://129.151.232.40:8443` | P155 |
| `sandbox-host` | `oracle-gustaw-janecki-ubuntu-flex-02` | `10.0.0.14:8443` | `https://132.226.205.205:8443` | [P157](../040-implementation-evidence/P157-sandbox-host.md) |
| `factory-sandbox` | `oracle-waclaw-dziwulski-ubuntu-micro-01` (Ubuntu 26.04 LTS, x86_64) | `10.0.0.30:8443` | `https://145.241.255.66:8443` | [P157 factory-sandbox](../040-implementation-evidence/P157-factory-sandbox.md) |

Each configuration pins `account: ubuntu`, its service root, `runnerd.sock`,
its own direct bind and public endpoint, TLS 1.3, server certificate, client
CA, principal map, and host-process adapter. The server private key is only the
owner-only file named by `secret_references.linux_server_private_key`; do not
print or commit it.

The direct client principal map is:

```text
/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
```

It maps the profile's verified direct certificate URI SAN to its configured
direct controller principal. The queued path has separate permanent
bridge/controller files under the Ubuntu `config/` directory. Direct mTLS does
not make the bridge available.

A future host can use a `version: 2` Linux configuration, which requires its
own `linux.remote_target_profile`. Every registered environment on that host
must permit that remote profile, and the Linux document rejects the Mac-only
`execution_contexts`, `remote_hosts`, and `mailboxes` sections. It needs
independent state, listener/certificates, key pin or direct mTLS material, and
P157 evidence; it must not copy any accepted host's state database or
secrets.

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
| Unmarked mailbox draft cleanup | 24 hours |
| Private ingress diagnostic cleanup | 7 days after observation |
| Marker-only residue | No ordinary age cleanup; review lifecycle status |

### What is configurable

The selected values above are validated owner-only YAML policy values, not
hardcoded operational choices. Put common defaults in the top-level `limits`
and `retention` sections; an environment can provide a narrower or different
validated `service_limits` object when that policy needs it. Configuration is
loaded at process start, so review a complete candidate and use the documented
Mac or Ubuntu installer rather than editing an active file in place.

```yaml
limits:
  active_sessions_per_host: 20
  running_commands_per_host: 4
  command_timeout: "30m"
  idle_timeout: "30m"
  session_max_lifetime: "4h"
  output_bytes_per_command: 104857600
  subscriber_buffer_bytes: 1048576
  persistence_queue_bytes: 16777216

retention:
  metadata_and_idempotency: "90d"
  output_events: "30d"
  mailbox_ack_grace: "24h"
  mailbox_unacked: "7d"
```

| Item | Configuration rule |
| --- | --- |
| `limits` fields shown above | Configurable subject to service-limit validation; the same fields may be supplied as an environment's `service_limits`. |
| `serialized_request_bytes` | Fixed at exactly `1048576` bytes if stated; it is not a tunable ceiling. |
| `script_bytes_per_request` | Fixed at exactly `131072` UTF-8 bytes if stated; it is not a tunable ceiling. |
| `retention.metadata_and_idempotency` | Configurable, but never below 90 days. |
| Other `retention` fields | Configurable positive durations. |
| Unmarked draft cleanup and private diagnostic cleanup | Fixed mailbox lifecycle behavior: 24 hours and seven days respectively, not fields in `retention`. |
| `durable_orphan_cleanup` | Per-inbox boolean policy for durable-proven marker-only residue; it is not a retention duration and defaults to `false`. |

The active-session and running-command limits apply independently to each
Runner authority/host. They are durable safety reservations, so a count can
remain nonzero until cleanup or process stop is confirmed. See
[how the four queue and slot gauges fit together](operations.md#how-the-four-queue-and-slot-gauges-fit-together)
for the lifecycle and the distinction between limits, queues, and warnings.

`runner-local doctor`, `runner-locald doctor`, and `runnerd doctor` validate
their respective runtime configuration. A doctor command writes a timestamp
health record to SQLite, so it is diagnostic rather than read-only. Use the
[operations runbook](operations.md) for command examples.
