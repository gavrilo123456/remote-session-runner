# BUG-033 — Initial uncertain remote dispatch is never observed again

## Status

**FIXED — PENDING VERIFICATION.** The source correction and incident-scoped
operator passed Mac affected-package tests and vet. Native delivery,
installed-service acceptance and the exact request's disposition remain
separate unfinished gates. Source tests alone do not establish either live
outcome.

## Observed behavior

Mac PRD Runner accepted GPU Services request
`req-gpu-f07-native-cli-smoke-20261117-bq1` at
`2026-11-17T15:15:54.247005Z` (authorization record 39313). At
`15:16:10.296962Z`, its outbox was revision 2, `accepted`, with delivery
`uncertain` and reason `remote_transport_uncertain`. It advertised no command
outcome, output, event cursor or teardown result.

The original identities are:

- Job `job-8ae87493c54d3f07e63c2b0b426fc2e1`.
- Session `sess-93c11db0aa0cd05d1c2a0bc768e266cb`.
- Command `cmd-d3b0357750b06d2caea304cc98971183`.

The persisted execution context is `ubuntu-current`, environment `sandbox-dev`,
target `remote/linux-host`. The environment name does not select Sandbox:
the configured Runner worker is `ubuntu@79.72.94.59`, hostname
`oracle-waclaw-dziwulski-ubuntu-micro-02`.

At 15:29 UTC, that worker's private API returned 404 for the exact job,
session and command under the mapped `queued_mac/tomasz.walczuk` controller.
At 15:31 UTC, `get_job` through the actual configured dispatcher key and pinned
known-hosts file returned `resource_not_found`, with SSH exit 0. Mac job and
command reads still exposed a local intent rather than execution evidence.

This does not prove that the smoke executed, exited, succeeded, or cleaned up.
A later independent successful Git pull does not establish this smoke's outcome.
The original transport failure detail is unavailable. The roughly sixteen-second
interval does not establish its cause.

## Confirmed source defect

`RemoteDriver.DispatchNext` selects recorded intents only. Initial transport
uncertainty removes an intent from that selection. The live `runner-local`
cycle observes already-accepted remote runs and submits, but has no production
path to observe initial uncertain dispatches. Restored connectivity therefore
does not by itself recover their original results.

The existing explicit `ReconcileIntent` is unsuitable as an observation:
its not-found branch can retry the original mutation while the idempotency
guarantee remains open. It must not be used to diagnose this no-replay request.

## Required correction and verification

Add a bounded, rotating observation pass for already-uncertain remote one-off
runs whose leases have expired. Other mutation types remain outside this correction. The pass
sends GET operations only. Matching authoritative evidence can
restore acceptance and normal strict result/event projection. Not-found,
timeouts, identity mismatches and active leases preserve uncertainty and must
not resend the mutation, reset its original uncertainty window, fabricate
terminal proof, or ACK anything. An atomic local guard must reject stale
observations that race an owner disposition or another transition.

Disposable tests must cover lost acceptance replies with and without actual
target admission, restart, strict result/event recovery, no-replay on 404,
timeouts, mismatches, batch rotation, active leases, stale observations and
truthful expiry. Runs left in `dispatching` by a crash before the uncertainty
transition remain outside this correction; do not reset or invent their
uncertainty history. Run affected-package tests, vet and native builds through the
required Mac → Gitea → exact GitHub mirror → clean Ubuntu fast-forward flow.

## Exact incident resolution boundary

If no original result can be recovered, the only truthful non-success response
is an explicitly authorized `indeterminate` disposition. The existing store
operation can preserve the exact request/intent/resource identities and lost
transport history, close dispatch atomically, and retain an unACKed terminal
response without inventing a command exit or teardown. An incident-scoped
operator needs verified bindings, an expired lease, current authority proof,
a consistent backup, stopped ingress/dispatcher guards and explicit approval
of the unknown-outcome disposition. Other requests, holds and capacities
remain outside scope. Do not repurpose the older hardcoded QAS closeout tool.

The user's uncommitted GPU project evidence remains unchanged. No request
replay, replacement, ACK or synthetic acceptance is part of this correction.

## Evidence

See [BUG-033 evidence](../040-implementation-evidence/BUG-033.md).
