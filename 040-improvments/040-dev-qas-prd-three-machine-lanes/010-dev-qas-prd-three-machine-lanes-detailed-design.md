# Detailed design: Dev, QAS, and PRD three-machine deployment lanes

**Status:** implementation design. It defines the next source phases but does
not authorize an installed-lane change until the capacity, quiescence, and
route gates in the phased plan pass.

## 1. Outcome and boundary

Runner needs three concurrently usable release lanes:

- **Dev** accepts source changes and is where new Runner behavior is developed
  and exercised across both Linux targets.
- **QAS** receives the exact Dev candidate and validates it independently.
- **PRD** remains the stable, accepted Runner that existing users can keep
  using while Dev and QAS change.

The existing unsuffixed installation becomes the compatibility baseline for
PRD. Creating Dev or QAS must never rename, stop, repoint, share state with,
or otherwise alter that installation. A lane is a release identity. It is not
an execution-environment label and a request cannot select it freely.

The file mailbox remains the integration boundary. Marker-last publication,
importer validation, SQLite authority, idempotency, event cursors, ACKs, and
conservative recovery retain their present behavior within each lane.

## 2. Fixed lane namespace

The following names are fixed. Installers derive them from the lane name and
reject an arbitrary root, service label, socket, or port supplied by an
operator or request.

| Resource | Dev | QAS | PRD compatibility baseline |
| --- | --- | --- | --- |
| Mac services | `runner-local-dev`, `runner-locald-dev` | `runner-local-qas`, `runner-locald-qas` | `runner-local`, `runner-locald` |
| Mac service root | `~/Library/Application Support/RemoteSessionRunner-dev` | `~/Library/Application Support/RemoteSessionRunner-qas` | existing `~/Library/Application Support/RemoteSessionRunner` |
| Linux unit | `runnerd-dev.service` | `runnerd-qas.service` | `runnerd.service` |
| Linux service root | `~/.local/share/remote-session-runner-dev` | `~/.local/share/remote-session-runner-qas` | existing `~/.local/share/remote-session-runner` |
| Direct HTTPS port on each Linux host | `8444` | `8445` | `8443` |
| Mac checkout role | authoritative source `dev` worktree | clean deployment worktree on `qas` | clean deployment worktree on `prd` |

PRD is explicit in configuration, health, status, evidence, and release
metadata even though its compatibility resource names remain unsuffixed. A
health response must contain both `release_lane` and `build_revision`; a
branch name alone is not deployment proof.

## 3. Configuration and routing model

Lane-capable configuration introduces a new versioned configuration form with
one required `release_lane` value: `dev`, `qas`, or `prd`. Version 2 remains
valid only for the current single-runtime compatibility installation; it is
never silently reinterpreted as a lane.

Each lane owns its own complete configuration, SQLite database, sockets,
mailboxes, logs, workspaces, temporary files, backups, and installed binary.
The configuration validates all of the following before a service starts:

1. The lane name agrees with the derived service root, service label/unit,
   socket namespace, and direct HTTPS port.
2. Every execution context points only to a remote profile in the same lane.
   For example, `dev/linux-host` cannot resolve to a QAS or PRD profile.
3. Every mailbox root belongs to exactly one lane; mailbox roots, durable
   exchange identities, events, and ACKs never cross lane boundaries.
4. Every remote profile has a lane-specific queued bridge and, where direct
   access is enabled, a lane-specific mTLS endpoint and controller map.
5. A service refuses a configuration whose controller identity, certificate,
   bridge socket, or endpoint profile names a different lane.

The first lane-aware Mac and Linux configuration schema must include fixture
coverage for invalid cross-lane roots, ports, profiles, controller identities,
and mailboxes. The existing V2 parser and active V2 configuration remain
unchanged until the PRD migration phase is explicitly scheduled.

## 4. Trust and transport isolation

The same Unix accounts continue to run the processes in this PoC, so lanes are
not a hostile-code isolation boundary. They are independent service and data
boundaries. The required transport separation is nevertheless real:

- Dev, QAS, and PRD use separate queued-SSH dispatcher keys, known-host files,
  forced-command wrappers, controller maps, bridge sockets, and manifests.
- Each lane uses distinct direct mTLS client identities and server/controller
  authorization. A Dev or QAS client is rejected before command creation when
  presented to a PRD endpoint, and vice versa.
- A Linux host runs three named units only after its capacity gate passes.
  Their state, run socket, listener, workspace, log, and retention paths are
  all under the lane-specific service root.
- Requests keep the existing configured-target model. They cannot provide a
  free-form lane, hostname, account, port, certificate, bridge, or path.

The current queued bridge installer hard-codes the unsuffixed root and socket.
It must become a lane-aware installer that derives those paths from the same
fixed lane registry as the service installers. It must not update an existing
different-lane bridge while installing or refreshing Dev or QAS.

## 5. Promotion and artifact contract

The authoritative Mac checkout remains the only source-edit location on
`dev`. A candidate is built and tested from a specific Dev SHA, then recorded
in an immutable release manifest containing at least the SHA, source archive
digest, binary digest per platform, lane-independent test evidence, and build
time.

Promotion is strictly one way:

```text
authoritative dev SHA + manifest
        │
        ├── deploy and accept Dev
        │
        ├── fast-forward same SHA to qas; deploy verified artifact; accept QAS
        │
        └── fast-forward same SHA to prd; deploy verified artifact; accept PRD
```

QAS and PRD use clean deployment worktrees and do not contain source edits or
merge commits. The Gitea primary and GitHub distribution mirror requirements
continue to apply before an Ubuntu host receives a new revision. A promotion
records the primary SHA, mirror SHA, installed SHA and binary digest on the
Mac, AMD64 host, and ARM64 host. A failed Dev or QAS candidate never changes
PRD; correction returns to Dev with a new manifest.

## 6. Capacity, safety, and migration gates

No Dev or QAS service is installed until a read-only capacity record covers the
Mac, the AMD64 Linux host, and the ARM64 Linux host. It records free bytes and
inodes, memory, current service/port inventory, retained-output and workspace
budgets, bridge and mTLS identity availability, route readiness, and all active
or queued Runner work.

The gate passes only when:

- every existing lane and target is live and ready, with no active, queued,
  uncertain, or unacknowledged work that the installation would touch;
- `8443`, `8444`, and `8445` have an unambiguous per-host ownership plan;
- planned peak usage for PRD + Dev + QAS, including SQLite/WAL, build staging,
  workspaces, output/event retention, logs, backups, and a rollback copy,
  leaves at least 30 percent free bytes and inodes on every host; and
- each new lane has distinct credential, controller, and mailbox material.

The earlier report of an ARM64 root filesystem near full is therefore an
explicit stop condition, not a warning to waive. Capacity is expanded or
retention budgets are reduced before lane installation. A transient remote
route failure or any unrelated live mailbox work also stops the preflight; it
is preserved and resolved through its own lifecycle before lane work resumes.

Dev and QAS are introduced as fresh roots. Existing mailbox files are not
moved. PRD migration is a separate, scheduled maintenance phase: validate the
candidate V3 PRD configuration, take a continuity backup, perform the normal
quiescent refresh, attest unchanged mailbox roots and durable state, and prove
the serving lane and revision. Rollback restores the prior unsuffixed PRD
binary/configuration only; it never copies Dev or QAS state into PRD.

## 7. Acceptance contract

Each lane requires source, static, installed, and live evidence. The live
proof covers both Linux architectures and includes a harmless native mailbox
exchange, terminal response, complete event prefix, exact ACK consumption, and
zero-work postflight. It must prove:

1. the response and each health endpoint identify the expected lane and build
   revision;
2. same-lane remote execution succeeds through its configured bridge;
3. direct mTLS and queued bridge requests from another lane are denied before
   command creation;
4. a Dev or QAS refresh leaves PRD process IDs, sockets, listener, database,
   mailbox trees, credentials, revision, and workload unchanged; and
5. the immutable promotion record links the same SHA and artifact digest from
   Dev through QAS to PRD.

Event-driven mailbox wake-up remains independent work. It may be considered
after lane adoption only when measured ingress latency requires it; it must be
implemented as a per-lane wake-up hint with periodic reconciliation retained.
