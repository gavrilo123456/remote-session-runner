# BUG-018 — Configured Mac-local mailbox context cannot be selected

## Summary

| Field | Value |
| --- | --- |
| Status | `RESOLVED` |
| Severity | High |
| Priority | P1 |
| Reported | 2026-10-10 |
| Discovered by | SlideStudio Logger deployer through the `slidestud-io` workspace mailbox |
| Owner | Unassigned |
| Affected component/path | Mac `runner-local` / `runner-locald` context selection, `slidestud-io` mailbox context mapping, and rejection diagnostics |
| Affected revision | Installed revision unknown after the recent Runner deployment/restart |
| Fixed revision | `3d01cf7f1f3b8845e8f3af98668a298b9f26a7e7` |
| Verification | Installed Mac acceptance passed at `2b75f5bfc0c0039fc29e0793637d99fda02bd4af` |

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
    "environment": "mac-dev",
    "execution_target": {"kind": "local", "profile": "mac-workstation"},
     "script": "printf 'mac-local-context-test\\n'"
   }
   ```

4. Publish the complete `0644` JSON first, then a new empty `0644` ready
   marker last.
5. Correlate the outbox by request ID. The expected result after this fix is a
   terminal `complete` response with a command ID, `request_override`, and the
   resolved `mac-dev` / `local/mac-workstation` pair.
6. Read events through the advertised cursor, then ACK that exact terminal
   response. Do not reuse a rejected request ID or key.

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
| 2026-10-10 | Investigation found the installed `slidestud-io` allow-list already includes context `mac-local`, whose wire tuple is `mac-dev` plus `local/mac-workstation`. The rejected requests incorrectly used the context name as both wire values. The resolver correctly rejected those unknown pairs, but its terminal result gave no safe correction. |
| 2026-10-10 | Resolved in `3d01cf7`; installed Mac acceptance passed at `2b75f5b` with a fresh explicit Mac-local request, complete event stream, and consumed ACK. |

## Root cause

`execution_contexts` names are configuration identifiers. The mailbox request
format accepts only an exact `{environment, execution_target}` pair. The
installed Mac configuration defines `mac-local` as:

```yaml
environment: mac-dev
execution_target: {kind: local, profile: mac-workstation}
```

Both reported requests supplied `environment: mac-local` and an invented
local profile. They therefore did not identify any configured context. This
was a correct fail-closed routing decision, but the generic rejection omitted
the safe symbolic configuration needed to correct it.

## Fix plan

1. Keep strict exact-pair selection; do not introduce aliases or route an
   unknown pair to a default target.
2. Expose a bounded list of a mailbox's allowed context names and symbolic
   environment/target pairs from the trusted configuration resolver.
3. Attach that list, together with the requested pair, only to terminal
   `environment_target_mismatch` responses. Exclude scripts, paths, host
   addresses, credentials, tokens, and environment values.
4. Add a regression test for the configured Mac-local pair and a mismatched
   pair, including a no-script-leak assertion.
5. Document the exact SlideStudio Mac-local tuple, run targeted and full Go
   tests, deliver through the Mac → Gitea → GitHub mirror → both Ubuntu
   fast-forward sequence, then install and prove a fresh harmless request.

## Resolution and verification

The resolver still rejects an unknown tuple before command allocation. For a
complete but unknown pair, it now returns `environment_target_mismatch` with
only these safe `error.details` fields:

- the requested symbolic environment and target;
- each allowed context's name, environment, and symbolic target.

It never includes request script content, repository or certificate paths,
host addresses, credentials, tokens, or environment-variable values.

Regression evidence:

- `TestBUG018MacLocalContextUsesConfiguredTupleAndSafelyExplainsMismatch`
  proves `mac-dev` / `local/mac-workstation` reaches allocation and a bad
  `mac-local` pair stays rejected without leaking the script.
- `TestBUG018AllowedMailboxExecutionContextsExposeOnlySymbolicConfiguredPairs`
  proves the production configuration exposes only the bounded symbolic data.
- Full `go test ./...` and the `bug018host` tagged compile gate passed.

Installed acceptance evidence, after rebuilding `runner-local` and
`runner-locald` at `2b75f5bfc0c0039fc29e0793637d99fda02bd4af`:

```text
request_id: req-bug018-slidestud-mac-local-18dd45eef912b820
command_id: cmd-00ad10ed071a9ccd6c6433411b79a83c
request_state: complete
command_state: succeeded
resolved: mac-dev / local/mac-workstation
output: BUG018_SLIDESTUD_MAC_LOCAL_OK; tomasz.walczuk
events: complete and non-truncated
ack: consumed
```

Mac, AMD64 Ubuntu, and ARM64 Ubuntu source checkouts were fast-forwarded to
the same `2b75f5bfc0c0039fc29e0793637d99fda02bd4af` revision before this
acceptance gate. The Linux daemons did not require restart because the runtime
change is confined to Mac mailbox context selection and response diagnostics.
