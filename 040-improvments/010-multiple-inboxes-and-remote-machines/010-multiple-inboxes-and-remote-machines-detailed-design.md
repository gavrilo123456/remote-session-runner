# Detailed design: configurable inboxes and named remote machines

**Status:** implemented historical design. P150--P159 delivered this
extension, and P157 separately accepted `sandbox-host`. This document records
the design contract used for that delivery; the current active topology is
three inboxes (`default`, `analytics`, `slidestud-io`), one local context, and
two accepted Ubuntu profiles. See [configuration](../../docs/configuration.md)
and [current-host evidence](../../docs/current-host-evidence.md) for current
paths and accepted-host scope.

## 1. Scope decisions

The extension has four user-visible capabilities:

1. Several named, owner-only file inboxes on the Mac.
2. A repository scope label attached to each inbox configuration.
3. A default execution context for each inbox.
4. An explicit, allow-listed execution-context override for a new mailbox run
   or session.

It also generalizes the current one remote machine into a registry of named
Ubuntu hosts. A host may be reached through the existing restricted queued SSH
bridge, through a named direct mTLS endpoint from the CLI, or through both.

The PoC stays deliberately small:

- Remote processes continue to run as `ubuntu`. Adding arbitrary remote Unix
  accounts is outside this extension.
- The only local target remains `local/mac-workstation`. There is no local-host
  scheduler or arbitrary local profile feature.
- Repository scope is configuration and audit metadata. It does not create a
  checkout, accept a path from a request, or authorize source materialization.
- Configuration is loaded at process start. Changing routes or inbox policy
  requires validating the complete file and restarting the Mac services.
- There is no automatic fallback between hosts, between queued and direct
  routes, or between remote and local execution.

## 2. Terms and identities

| Term | Meaning |
| --- | --- |
| **Inbox ID** | A stable lowercase configuration name, such as `website` or `analytics`. |
| **Mailbox root** | The owner-only directory containing an inbox's `inbox`, `outbox`, `events`, `acks`, and `diagnostics` children. |
| **Execution context** | A named pairing of one environment and one immutable target `{kind, profile}`. |
| **Remote profile** | A named Ubuntu host identity, for example `ubuntu-build-1-host`. It selects one configured bridge and, optionally, one mTLS endpoint profile. |
| **Repository scope** | Zero or more configured repository aliases associated with an inbox. It labels and limits intended use; it is not an untrusted source path. |
| **Client request ID** | The file-name-safe request ID written by a mailbox client. It is scoped by Inbox ID in durable state. |

The resource target stored for a session, command, job, intent, and remote
projection remains the existing `{kind, profile}` identity. A context only
resolves the choice at new-work acceptance; it never becomes a mutable routing
pointer for an existing resource.

## 3. Configuration model

### 3.1 Compatibility

The existing owner-only `version: 1` configuration remains valid. It is read as
one implicit inbox named `default` at the existing path and one implicit remote
host profile named `linux-host`.

`version: 2` enables the new registries. It must be rejected unless every
reference is complete, unique, and passes the applicable no-symlink, owner,
and mode checks. The legacy `default` mailbox remains at its selected service
root path. A non-default mailbox may use either its selected service-root path
or a separately configured owner-safe absolute path; the rules are defined in
the next section.

### 3.2 Conceptual Mac configuration

This is the pre-delivery target-shape example. Its `ubuntu-build-2` names and
documentation IP address are historical, not active configuration. Exact
current names and paths are frozen by the implementation and documented in the
configuration reference.

```yaml
version: 2
mac:
  account: tomasz.walczuk
  service_root: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner
  api_socket: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock
  locald_socket: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock
  sqlite: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db

execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  ubuntu-current:
    environment: linux-dev
    execution_target: {kind: remote, profile: linux-host}
  ubuntu-build-2:
    environment: ubuntu-build-2-dev
    execution_target: {kind: remote, profile: ubuntu-build-2-host}

remote_hosts:
  linux-host:
    account: ubuntu
    queued_bridge:
      host: 129.151.232.40
      port: 22
      known_hosts: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts
      private_key: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519
    direct_endpoint:
      name: linux-poc
      url: https://129.151.232.40:8443
      server_ca: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem
      client_certificate: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem
      client_private_key: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key
  ubuntu-build-2-host:
    account: ubuntu
    queued_bridge:
      host: 203.0.113.20
      port: 22
      known_hosts: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ubuntu-build-2_known_hosts
      private_key: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ubuntu-build-2_dispatcher_ed25519

mailboxes:
  default:
    root: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
  analytics:
    root: /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics
    repository_aliases: [analytics-dbt]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current, ubuntu-build-2]
```

The sample IP address is documentation-only. A real remote-host entry is not
ready until its own Ubuntu service, mTLS materials or bridge, and host gate are
installed and tested.

### 3.3 Configuration validation

- Inbox IDs, context names, remote profile names, and direct endpoint names
  are bounded safe identifiers and unique in their namespace.
- Mailbox roots are unique owner-only directories and neither may equal nor
  contain another configured root. The existing default root remains valid and
  is never moved automatically. A non-default root inside the service root
  must use its selected `mailboxes/<inbox-id>` path. A non-default root outside
  the service root may be any clean absolute path, provided its parent already
  exists and the whole existing ancestor chain is made of real directories
  without group or other write permission. The immediate parent must be owned
  by the selected Mac user. This permits a repository-local mailbox while
  rejecting unsafe shared paths such as `/tmp`. The configuration rejects roots
  that are equal or nested after conservative case and Unicode normalization,
  so a case-insensitive filesystem cannot bind two inboxes to one tree.
- Runner never creates or changes an external root's ancestors. It creates or
  verifies only the configured root and its `inbox`, `outbox`, `events`,
  `acks`, and `diagnostics` children. Each must be a current-user-owned real
  directory at mode `0700`. Before the existing LaunchAgents are quiesced, the
  installer performs
  a non-mutating descriptor-based preflight of the parent chain and any tree
  that already exists. It never creates a candidate root during that stage.
  After ingress is quiesced and the retained-work check passes, the irreversible
  activation records the complete candidate mailbox set durably **before** it
  creates or verifies a newly visible external tree. Descriptor-relative
  `O_NOFOLLOW` traversal and creation reject symlink substitution while that
  tree is prepared. If a post-registration filesystem step fails, the staged
  candidate remains available for safe repair; the prior LaunchAgents are not
  revived against the changed durable registry.
- Each context names one configured environment and a target permitted by that
  environment's policy.
- Each mailbox has at least one allowed context, and its default is in that
  list.
- A remote profile has exactly one pinned queued-bridge definition if it is
  usable from any mailbox. A remote profile may additionally have one direct
  mTLS definition for the CLI.
- Every private-key, certificate, and known-host value is an absolute,
  owner-only file reference. No credential material appears in configuration,
  responses, logs, commits, or diagnostics.
- A remote profile's account must be `ubuntu` in this PoC.

## 4. Mailbox request and resolution contract

The marker-last filesystem protocol is unchanged. A client writes immutable
JSON, then an empty `.ready` marker. It reads the matching response and event
prefix, then writes the matching acknowledgement. Every mailbox root and child
directory remains exact mode `0700`. Native `mailboxclient` request and ACK
pairs are exact `0600`; a direct workspace-file producer may publish otherwise
valid request or ACK pairs at exact `0644`. Runner keeps response and event
projections at exact `0600`. The `0644` compatibility path still requires
complete JSON before the empty marker and does not claim native exclusive
creation, no-follow, sync, or crash-durability properties.

For `run` and `create_session`, `environment` and `execution_target` are
treated as one pair:

| Request fields | Result |
| --- | --- |
| Both omitted | Resolve the inbox's configured default context. |
| Both present | Treat them as an override. Resolve only when they match one allowed context for that inbox. |
| Exactly one present | Reject before durable resource acceptance. |
| Present but unknown, mismatched, or not allowed | Reject before durable resource acceptance. |

`submit_command` keeps the existing session target. It has no execution
override. A caller that needs a different machine creates a new session or
uses a new `run` request.

The response protocol remains additive and includes:

```json
{
  "inbox_id": "analytics",
  "execution_selection_source": "inbox_default",
  "resolved_execution_target": {"kind": "remote", "profile": "linux-host"},
  "resolved_environment": "linux-dev"
}
```

The response always uses the client's original `request_id`, never the
internal scoped durable ID.

## 5. Durable mailbox isolation

Multiple roots cannot share the existing global request and idempotency
namespace. The implementation therefore persists the following three values
for every mailbox exchange:

- `mailbox_id` — the configured inbox that accepted it;
- `client_request_id` — the original filename/request ID; and
- an internal stable exchange ID used by existing foreign keys and cleanup
  records.

The internal ID is deterministically derived from the Inbox ID and client
request ID. It is never exposed to a mailbox client. Mailbox mutation keys are
similarly scoped before they reach local intents, jobs, commands, and remote
bridge frames. This permits two inboxes to use the same client request or
idempotency key without sharing work or replies.

All persistence and maintenance operations select the mailbox namespace:

- receipt acceptance and idempotency lookup;
- response publication and crash recovery;
- acknowledgement recording;
- event-file references and retention cleanup; and
- pending/accepted reconciliation.

Existing rows migrate to `mailbox_id: default` with their present request ID as
the client request ID. No existing outbox or event file is moved.

## 6. Remote machine routing

The Mac Router owns a map from immutable remote target profile to a pinned
restricted-SSH caller. It chooses the caller from the target persisted in the
intent for every dispatch, read, event replay, reconciliation, and health
probe. A missing or unhealthy profile fails that profile's work; it never
falls through to another host.

Each configured bridge preserves the current restrictions:

- fixed `runner-ssh-bridge --stdio` command;
- no PTY, forwarding, SSH config, or agent use;
- a dedicated private key and pinned known-host file; and
- the Ubuntu forced-command/controller mapping on the target host.

Direct CLI endpoint profiles are also bound to exactly one remote target
profile. The CLI rejects a direct request when `--endpoint` and
`--profile` name different hosts. The direct and queued routes remain separate
controller authorities, as in the current implementation.

## 7. Runtime composition

`runner-local` creates one mailbox runtime per configured Inbox ID. A runtime
contains its own importer, outbox, event-file projector, acknowledgement
importer, session processor, and artifact cleaner, all sharing the existing
Mac authority database and API server.

The worker cycle processes every configured mailbox and dispatches local and
remote work through the existing shared authority. Metrics report both a total
mailbox backlog and per-inbox counts. Readiness reports each configured remote
route separately; one ready remote host does not hide a degraded route to
another host.

## 8. Delivered migration and operational behavior

P150--P156 delivered the V2 registry, routing, mailbox namespace, runtime,
installation, and documentation path. P157 separately accepted `sandbox-host`;
P158 added the external SlideStudio root; and P159 added direct-workspace
`0644` publication. The active configuration therefore reflects the migration
steps that were originally proposed here.

Future inbox additions still follow the same constraints: validate a reviewed
owner-only V2 candidate, protect retained work during the documented Mac
refresh, and independently install and accept every additional remote host
before routing work to it.

An inbox rename or removal does not retarget existing accepted work. The
configuration loader rejects removal of an inbox with pending unacknowledged
state unless an explicit later migration procedure handles it.

## 9. Delivered proof and continuing boundary

Hermetic tests must prove:

- legacy configuration produces the current default mailbox and host behavior;
- two inboxes can reuse client IDs and idempotency keys without crossing
  receipt, response, acknowledgement, event, or cleanup state;
- defaults and valid overrides resolve to the intended target before durable
  acceptance;
- partial and disallowed overrides leave no intent or remote work;
- a session remains on its original resolved target after config defaults
  change;
- a profile-specific fake bridge never receives work for another profile; and
- a direct endpoint profile cannot address a different remote target profile.

The delivered gates first used a non-default inbox against `linux-host`, then
accepted the second physical host through P157 and used it in P158/P159. A
future physical host remains **NOT RUN** until its own P157 deployment and
end-to-end route test pass.
