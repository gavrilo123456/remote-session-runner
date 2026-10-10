# BUG-018 — Configured Mac-local mailbox context cannot be selected

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` |
| Severity | High |
| Priority | P1 |
| Reported | 2026-10-10 |
| Discovered by | SlideStudio Logger deployer through the `slidestud-io` workspace mailbox |
| Owner | Unassigned |
| Affected component/path | Mac `runner-local` / `runner-locald` context selection, `slidestud-io` mailbox context mapping, and rejection diagnostics |
| Affected revision | Installed revision unknown after the recent Runner deployment/restart |
| Fixed revision | Not yet identified |
| Verification | Not run |

## Reported behavior

The mandatory SlideStudio route for scoped local Git writes is an explicit
Mac-local mailbox request. Two fresh, syntactically valid V1 `run` requests
were consumed and rejected before command allocation with:

```json
{
  "request_state": "rejected",
  "error": {
    "code": "environment_target_mismatch",
    "message": "environment and execution target do not identify a configured context"
  }
}
```

The response does not disclose the accepted environment/target combinations,
the requested values, a supported-profile list, or a safe correction. It
therefore leaves an agent unable to select the required Mac-local executor
without guessing.

This is separate from BUG-017. These requests used the repaired canonical V1
`script` field and reached a different, explicit context-mapping rejection.

## Expected behavior

At least one of these must hold:

1. The documented/allowed `slidestud-io` Mac-local context is accepted for a
   V1 `run` request; or
2. `environment_target_mismatch` reports safe, actionable diagnostics: the
   requested environment and target shape plus the allowed symbolic context
   combinations for that mailbox. It must not disclose paths, certificates,
   tokens, or command content.

The file-only mailbox is the mandated control boundary. An operator must be
able to use its required Mac-local Git route without falling back to direct
terminal execution.

## Safe evidence

### Workspace and policy context

```text
Mac: AMAK2KJ6X9JJJ
Workspace: /Users/tomasz.walczuk/projects/slidestud.io
Mailbox: slidestud-io
Mailbox root: tmp/mailbox-
Operation: run
Request representation: canonical V1 script string
Requested action: scoped Git add, commit, and push of three reviewed Logger
                  preflight-repair files
Secret handling: no token, key, or secret value was placed in either request
```

The repository policy requires those exact scoped Git writes to run only via
an explicit Mac-local mailbox context. It prohibits a direct shell fallback.

### Rejected request 1

```text
request_id:      req-codex-commit-push-logger-preflight-reuse-fix-20261010-172
idempotency_key: slidestud-logger-preflight-reuse-fix-20261010-172
environment:     mac-local
target kind:     local
target profile:  mac-local
outbox state:    rejected, response_revision=1
```

The request used a complete JSON file followed by a new zero-byte `0644`
ready marker. Its script changed only the three explicit reviewed paths:

```text
.gitea/workflows/verify-logger-cutover-preflight.yml
infra/infra-docker-cmdb/ci/validate-logger-cutover-preflight.py
infra/infra-docker-cmdb/ci/tests/test_logger_cutover_preflight.py
```

No command was allocated or executed.

### Rejected request 2

```text
request_id:      req-codex-commit-push-logger-preflight-reuse-fix-20261010-173
idempotency_key: slidestud-logger-preflight-reuse-fix-20261010-173
environment:     mac-local
target kind:     local
target profile:  mac-host
outbox state:    rejected, response_revision=1
```

This was one controlled retry with a new identity. It retained the same
environment, canonical V1 `script` representation, exact three-path scope,
and marker-last publication protocol; only the profile was changed to the
plausible host-name counterpart. It received the same rejection before any
command started.

### Impact

- **Blocked:** committing and pushing the narrow Logger preflight idempotency
  repair, which is needed before the protected Gitea flow can resume.
- **Not changed:** local Git index/commit history, GitHub, Gitea, Vault,
  target hosts, and Logger runtime. Both requests were rejected before
  command allocation.
- **Unsafe workaround prohibited:** direct terminal `git add`, `git commit`,
  or `git push`; direct Runner APIs; and remote-host execution all violate the
  SlideStudio mailbox boundary.

## Exact safe reproduction

1. Configure or select the `slidestud-io` mailbox.
2. Create a new request ID and idempotency key. Do not reuse either request
   listed above.
3. Publish a V1 request with a harmless script, for example:

   ```json
   {
     "request_id": "<new-id>",
     "idempotency_key": "<new-key>",
     "repository_alias": "slidestud-io",
     "operation": "run",
     "environment": "mac-local",
     "execution_target": {"kind": "local", "profile": "mac-local"},
     "script": "printf 'mac-local-context-test\\n'"
   }
   ```

4. Publish the complete `0644` JSON first, then a new empty `0644` ready
   marker last.
5. Correlate the outbox by request ID. Current behavior is immediate terminal
   rejection with `environment_target_mismatch`, no command ID, and no event
   stream.
6. Repeat at most once with a new identity and the Runner's documented
   Mac-local tuple. Do not guess through arbitrary profiles.

## Investigation guidance

1. Inspect the installed mailbox context registry used by `runner-local` and
   `runner-locald`, not merely source defaults. Confirm the exact accepted
   triples of mailbox name, `environment`, and `execution_target`.
2. Compare that registry to the documented `slidestud-io` allowed-execution
   policy, which has previously named a Mac-local execution route.
3. Determine whether a Runner deployment renamed the Mac profile, removed its
   environment binding, or changed the request-model mapping for local
   execution.
4. Preserve strict rejection of unknown contexts; the fix must not silently
   route a request to a different execution environment.
5. Improve the safe rejection diagnostic with only symbolic configuration
   names and structural fields. Never echo the script, tokens, certificate
   paths, private host details, or environment variables.

## Required fix and verification gate

1. Restore or document one accepted explicit Mac-local context for the
   `slidestud-io` mailbox.
2. Keep the V1 `script` contract and marker-last inbox protocol unchanged.
3. For a mismatch, return the safe requested symbolic tuple and allowed
   symbolic tuples for that mailbox.
4. Add regression coverage for the exact `mac-local` / local-target request
   shape and for rejection redaction.
5. Install the fix and prove it using a fresh harmless Mac-local request with
   a new request ID/key. It must produce a command ID, complete terminal
   outbox response, complete non-truncated event output, and a consumed exact
   ACK. Only after that may the pending Logger Git write be retried once.

## History

| Date | Change |
| --- | --- |
| 2026-10-10 | Reported after two new canonical-V1 Mac-local requests were rejected before command allocation with no actionable configured-context information. |
