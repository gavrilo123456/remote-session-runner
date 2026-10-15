# Phased implementation plan: Dev, QAS, and PRD three-machine deployment lanes

**Status:** implementation plan. It is deliberately serial. A failed or
incomplete gate stops the next phase and leaves the existing unsuffixed Runner
unchanged.

## Operating rule for every phase

All tracked changes originate in the authoritative Mac `dev` checkout. For a
completed phase, inspect the intended diff; commit it on `dev`; push to local
Gitea; verify the automatic GitHub mirror has the same SHA; and fast-forward
each clean Ubuntu checkout before host validation. Record the Mac, Gitea,
GitHub, AMD64, and ARM64 SHAs, source tests, installed tests, live evidence,
and rollback state.

Never edit tracked source in an Ubuntu checkout. Never use a lane installer to
repair active or uncertain existing work. Preserve mailbox evidence and use a
supported terminal disposition before any relevant service refresh.

## L0 — capacity and route baseline

**Deliverable:** a read-only lane-capacity record for Mac, AMD64, and ARM64.

1. Record disk bytes/inodes, memory, CPU, existing listeners, service roots,
   workspaces, log/output retention, build staging, backups, direct endpoint
   reachability, queued-bridge identity, and current health/build revision.
2. Record all active, queued, uncertain, and unacknowledged work. Do not ACK,
   retry, recover, remove, or restart it as part of this phase.
3. Set a per-lane resource budget and prove PRD + Dev + QAS peak consumption
   leaves at least 30 percent free bytes and inodes on every host.
4. Stop if a required route is degraded, any shared host has insufficient
   capacity, or the preflight cannot distinguish listener ownership.

**Exit gate:** all three machines have a signed-off inventory and the detailed
design's capacity gate is green. This phase creates no lane service, key,
certificate, mailbox, or deployment.

## L1 — lane-aware configuration and health contract

**Deliverable:** a versioned lane configuration schema and health/status
metadata.

1. Add `release_lane` validation for `dev`, `qas`, and `prd`, retaining V2
   compatibility for the present unsuffixed runtime.
2. Validate fixed roots, labels, units, sockets, ports, mailbox roots, target
   profiles, and controller identities for each lane.
3. Add `release_lane` to Mac and Linux health/status output alongside the
   immutable build revision.
4. Add fixtures and negative tests for every cross-lane configuration attempt.

**Exit gate:** hermetic schema, health, migration, and negative-isolation tests
pass. No installer or live service changes occur in this phase.

## L2 — lane-aware installer and bridge foundation

**Deliverable:** parameterized-but-fixed Mac, Linux, and queued-bridge
installers plus unit/LaunchAgent templates.

1. Accept an explicit lane and derive only the documented root, label/unit,
   socket, port, log, workspace, and configuration names.
2. Reject any mismatch between selected lane, configuration, Git revision,
   artifact manifest, or pre-existing different-lane resource.
3. Make queued bridges lane-specific, including their key, known-host file,
   forced wrapper, socket, controller map, and manifest.
4. Add dry-run/preflight commands that prove no PRD path will be written by a
   Dev or QAS invocation.

**Exit gate:** installer fixture tests prove correct derivation, refusal of
cross-lane state, and rollback after a staged start failure. The unsuffixed
service is not restarted.

## L3 — immutable release manifest and promotion controls

**Deliverable:** a release manifest and verified deployment-worktree flow.

1. Build one platform artifact set from an exact `dev` SHA and record source
   archive and binary digests.
2. Create clean deployment-only `qas` and `prd` worktrees, fast-forwarded only
   from the accepted source SHA through the Gitea-primary/GitHub-mirror path.
3. Require installers to verify the manifest's SHA and platform binary digest
   before activation, then attest the serving revision and lane afterwards.
4. Add promotion tests that reject rebuild-from-different-SHA, merge commits,
   altered manifests, and a QAS/PRD ref that differs from the accepted Dev SHA.

**Exit gate:** one artifact manifest proves the same source identity can be
carried through all lanes without deploying it to PRD.

## L4 — deploy and accept Dev

**Deliverable:** an isolated Dev control plane plus one Dev service on each
Linux architecture.

1. Provision Dev-only roots, credentials, bridges, endpoints, ports, mailbox
   roots, and controller authorization after a fresh L0 green preflight.
2. Install Dev on Mac, AMD64, and ARM64 without stopping or modifying PRD.
3. Run source gates and then fresh native mailbox acceptance through each Dev
   route, with complete output/event/ACK proof and zero-work postflight.
4. Attempt cross-lane direct and queued access to PRD; require denial before
   command creation.

**Exit gate:** Dev is live, lane-attested, and isolated; PRD's revision,
services, sockets, database, mailboxes, and workload are unchanged.

## L5 — promote, deploy, and accept QAS

**Deliverable:** an independently isolated QAS lane running the exact accepted
Dev artifact.

1. Fast-forward the QAS deployment worktree to the accepted Dev SHA and verify
   its manifest digest.
2. Provision QAS-only resources and install on the Mac, AMD64, and ARM64.
3. Repeat the L4 positive route tests and deny Dev-to-QAS, QAS-to-Dev, and
   QAS-to-PRD cross-lane control attempts.
4. Prove a QAS restart or failed candidate leaves Dev and PRD unchanged.

**Exit gate:** QAS and Dev run concurrently, both are healthy and isolated,
and PRD remains on its prior accepted revision.

## L6 — scheduled PRD lane migration and promotion

**Deliverable:** the unsuffixed stable installation explicitly identifies as
PRD and receives only a QAS-accepted manifest.

1. Schedule a maintenance window, take the continuity backup, and run the
   documented quiescence/retained-work preflight.
2. Migrate the unsuffixed configuration to explicit `release_lane: prd`
   without changing its root, service labels, ports, mailboxes, or durable
   state paths.
3. Install the exact QAS-accepted artifact and attest lane, revision, manifest
   digest, mTLS, bridge, mailbox, and recovery health on all three machines.
4. Run the PRD native mailbox acceptance and the cross-lane denial gates.

**Exit gate:** the PRD promotion has exact Dev → QAS → PRD provenance and a
tested rollback to the previous unsuffixed PRD artifact/configuration.

## L7 — operate and improve

Set per-lane capacity thresholds, backup/restore drills, retention budgets,
and promotion evidence retention. Revisit event-driven mailbox wake-up only
after measuring latency and backlog in the isolated lanes; it remains a
best-effort wake-up hint with periodic reconciliation, never a delivery path.
