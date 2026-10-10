# BUG-017 — Valid mailbox run requests are rejected without actionable schema diagnostics

## Summary

| Field | Value |
| --- | --- |
| Status | `FIX IMPLEMENTED — deployment and installed acceptance pending` |
| Severity | High |
| Priority | P1 |
| Reported | 2026-10-10 |
| Discovered by | SlideStudio Logger deployer through the `slidestud-io` mailbox |
| Owner | Unassigned |
| Affected component/path | Mac `runner-local` / `runner-locald` mailbox ingress and the `slidestud-io` workspace mailbox contract |
| Affected revision | Installed revision unknown after the most recent Remote Session Runner deployment/restart |
| Fixed revision | N/A |
| Verification | Pending a fresh remote `run` request that reaches a terminal outbox result |

## Reported behavior

Fresh, syntactically valid JSON requests for the documented `run` operation are
consumed from the SlideStudio workspace inbox and immediately rejected during
`ingress_validation` as `invalid_request_schema`. The diagnostic contains no
field-level error, expected schema version, accepted command representation, or
compatibility guidance.

This blocks all protected remote control-plane work, including the read-only
Gitea status call needed to continue the Logger DEV delivery. The requests did
not execute: `accepted=false` and `executed=false`.

## Expected behavior

At least one of these must hold:

1. A request conforming to the published/previously accepted mailbox `run`
   schema is accepted and dispatched to the selected remote profile; or
2. an ingress rejection identifies the failing field and expected type/version
   precisely enough for a client to correct a fresh request without guessing.

The mailbox is the required execution boundary for SlideStudio. A generic
schema error is not sufficient when a Runner update changes a live client
contract.

## Safe reproduction evidence

### Environment

```text
Mac: AMAK2KJ6X9JJJ
Workspace: /Users/tomasz.walczuk/projects/slidestud.io
Mailbox root: tmp/mailbox-/
Mailbox name: slidestud-io
Requested environment: sandbox-dev
Requested target: remote / sandbox-host
Operation: run
Remote action: read-only Gitea GET calls for Actions run 4372 and its jobs
Token handling: remote command reads the approved remote token path; no token
                value is present in the request, diagnostics, or this record
```

### Preconditions and service evidence

The operator restarted both Mac LaunchAgents immediately before the retries:

```bash
# Machine: Mac
launchctl kickstart -k "gui/$(id -u)/com.remote-session-runner.local" &&
launchctl kickstart -k "gui/$(id -u)/com.remote-session-runner.locald"
```

The post-restart output reported both services `state = running` with PIDs
`51317` and `51320`. The Gitea Actions runner was also separately restarted
on `ubuntu-30150` and was `active (running)` at 2026-10-10 14:31:08 UTC.

### Three fresh requests, each rejected before execution

Every request used a new `request_id` and idempotency key, complete JSON first,
and a zero-byte `.ready` marker last. They were created through the native file
publisher, not a shell redirect, direct API, SSH, or Runner API. All selected
the same explicit `sandbox-dev` / `remote:sandbox-host` target.

| Request | Command field attempted | Outcome |
| --- | --- | --- |
| `req-codex-monitor-logger-preflight-history-source-validation-20261010-65` | object: `{ "argv": ["/bin/bash", "-lc", "…"], "cwd": "/home/ubuntu" }`; `timeout_seconds: 90` | ingress rejected at `2026-10-10T14:38:16.99574Z` |
| `req-codex-monitor-logger-preflight-history-source-validation-20261010-66` | string containing the same bounded shell program; `timeout_seconds: 90` | ingress rejected at `2026-10-10T14:39:49.991899Z` |
| `req-codex-monitor-logger-preflight-history-source-validation-20261010-67` | array: `["/bin/bash", "-lc", "…"]` | ingress rejected at `2026-10-10T14:53:18.781572Z` |

All three diagnostic files contain the same safe result shape:

```json
{
  "lifecycle_phase": "ingress_validation",
  "accepted": false,
  "executed": false,
  "code": "invalid_request_schema",
  "message": "request does not satisfy the mailbox request format"
}
```

No outbox result or event stream was created for any of the three attempts,
which is correct for an ingress rejection. The diagnostic route itself works;
the problem is the missing actionable contract error and apparent incompatibility
with all known `run` command encodings.

## Exact safe reproduction

1. Start from the configured `slidestud-io` mailbox.
2. Create a new JSON request with:

   ```json
   {
     "request_id": "<new-id>",
     "idempotency_key": "<new-key>",
     "operation": "run",
     "repository_alias": "slidestud-io",
     "environment": "sandbox-dev",
     "execution_target": {"kind": "remote", "profile": "sandbox-host"}
   }
   ```

3. Add a harmless command which performs no writes, for example `uname -a` or
   a read-only Gitea Actions GET using the remote token path without printing
   the token.
4. Publish the complete JSON with mode `0644`, then publish a new empty `0644`
   `<request_id>.ready` marker.
5. Observe that the importer consumes the request and creates
   `diagnostics/<request_id>.json` with `invalid_request_schema`; no command is
   allocated or run.

Repeat only with new identities. Do not reuse any of the Logger request IDs or
idempotency keys listed above.

## Impact and scope

- **Blocked:** all SlideStudio operations that must use the mailbox: protected
  Gitea state inspection, workflow dispatch/monitoring, Vault contract checks,
  and deployment verification.
- **Not affected by this evidence:** direct interactive terminal execution,
  protected Gitea itself, target hosts, Vault state, Logger runtime, and source
  repositories. None was altered by these rejected requests.
- **Known safe state:** Logger source history repair was already delivered
  through review; source validation run `4372` existed before these requests.
  This bug blocks observing and advancing that flow, not the already completed
  source change.
- **Workaround:** none that respects the file-only mailbox boundary. Do not
  fall back to direct SSH, curl, a direct Runner API, or manual Gitea changes.

## Investigation notes

1. Earlier mailbox work had accepted `run` requests and produced terminal
   outbox/events. The current invalid requests follow the same identity,
   marker-last, target-selection, and no-secret principles.
2. The generic error began after a period of repeated Runner deployment/restart
   work. Installed Runner revision and active request schema version were not
   surfaced in the diagnostic.
3. The failures are independent of the remote command: all three variants were
   rejected before execution, so Gitea, networking, token authorization, shell
   behavior, and the Logger workflow cannot be causal for this specific fault.
4. `runner-local` and `runner-locald` were restarted successfully immediately
   before the final attempt; the rejection persisted afterwards.

## Root cause

The canonical mailbox V1 `run` shape uses `script`, a JSON string. It does not
define `command`, `argv`, or `cwd`. The three reported documents therefore
failed V1 schema validation before any remote work was allocated:

- the object and string `command` forms had no required `script` field and an
  unsupported `command` field;
- the array form was not a string `script`; and
- `cwd` is not a V1 field. A working-directory change belongs in the script,
  for example `cd /home/ubuntu && <command>`.

The importer correctly kept those inputs out of execution, but it discarded
the schema failure structure before creating its durable rejection record. The
private diagnostic consequently preserved only the generic fixed message, so a
publisher could not determine the correction without inspecting source code.

## Fix plan

1. Preserve the V1 `script`-only execution contract and do not reinterpret an
   `argv` object or arbitrary `cwd` as executable input.
2. When a safe V1 `run` JSON object fails schema validation, derive only a
   bounded structural correction: a fixed JSON pointer, expected and received
   **types**, schema version, canonical `script` representation, and a fixed
   redacted minimal valid request. Never retain a request value or parser text.
3. Freeze that correction inside the existing durable private diagnostic so
   restart recovery projects the identical artifact. Validate it in the store,
   diagnostic JSON schema, and native mailbox client.
4. Document the V1 `script` field and direct correction flow. Add regressions
   for the reported command-object form, redaction, canonical script dispatch,
   and native diagnostic reading.
5. Deploy the Mac ingress change, then publish one fresh harmless
   `slidestud-io` request with a new request ID/key, explicit
   `sandbox-dev`/`sandbox-host` selection, and `script: "uname -a"`. Verify a
   terminal outbox, events, and exact ACK without replaying any reported Logger
   request.

## Implementation record

- Source fix: static `schema_detail` in `invalid_request_schema` diagnostics;
  it is request-content-free and only appears for a rejected V1 `run` shape.
- Canonical execution remains `script: "..."`; `command`, `argv`, and `cwd`
  remain deliberately unsupported.
- Hermetic mailbox, native-client, store, and full-suite tests passed. The
  installed sandbox mailbox acceptance remains the closing gate.

## Required fix

1. Identify the canonical request schema currently deployed for `operation:
   run`, including the schema/version field (if any), command field name and
   type, timeout/cwd fields, and allowed execution-target form.
2. Restore backward compatibility for the previously documented client shape
   where practical, or ship a coordinated client/documentation update.
3. Make `invalid_request_schema` diagnostics disclose safe field-level
   validation failures: JSON pointer/path, expected type/value, received type,
   supported schema versions, and a redacted minimal valid example. Do not
   echo secrets or entire commands.
4. Add an installed end-to-end acceptance test using the `slidestud-io`
   mailbox and explicit `sandbox-dev` / `remote:sandbox-host` target. It must
   create JSON then zero-byte marker, run a harmless command, produce terminal
   outbox/events, and consume an ACK.

## Fix and verification gate

Do not close this record based on unit tests, a LaunchAgent restart, or a
schema-only fixture. Verification requires a fresh remote mailbox request with
a new request ID and idempotency key that:

1. is accepted at ingress;
2. reaches `command_started` and a terminal event;
3. produces terminal outbox state with complete, non-truncated output;
4. is ACKed using the exact response revision and event cursor; and
5. allows the Logger read-only Gitea run-status request to complete without
   changing Gitea, Vault, or target-host state.

## History

| Date | Change |
| --- | --- |
| 2026-10-10 | Reported after three independently shaped, fresh, read-only `run` requests were rejected at ingress following successful Mac Runner restarts. |
