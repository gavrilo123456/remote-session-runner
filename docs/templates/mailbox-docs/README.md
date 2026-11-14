# Using the {{PROJECT_NAME}} Runner mailbox

Read this reference on first use, when a request fails validation, or when you
are unsure about a result. An agent that already knows this workflow need not
reread it for every request. The project's `{{PROJECT_ROOT}}/AGENTS.md` remains
mandatory and defines which actions and targets are permitted.

This mailbox-root `docs/` directory contains documentation only. Never put request
JSON, ACKs or `.ready` markers here. Publish requests directly in `inbox/`
and ACKs directly in `acks/`, following the paths below. Reading these examples
does not execute them or grant permission for additional work.

## Your configured route

| Setting | Value |
| --- | --- |
| Mailbox root on the Mac | `{{MAILBOX_ROOT}}` |
| Mailbox ID and repository alias | `{{PROJECT_NAME}}` |
| Default execution | `{{LINUX_CONTEXT}}` → `remote/{{LINUX_PROFILE}}` |
| Linux address and account | `{{LINUX_ADDRESS}}`, user `ubuntu` |
| Canonical source checkout | `{{PROJECT_ROOT}}` on the Mac |
| Mac exception | Reviewed Git writes only, using the complete pair in example 2 |

Normal requests **omit both** `environment` and `execution_target`; Runner
uses the mailbox's configured Linux default. A context name is not an
environment ID. Do not invent selection fields or substitute Sandbox/Factory.
The filesystem root selects the mailbox: do not add an `inbox_id` field.
`repository_alias` identifies the project for policy/audit; it does not choose
a checkout or working directory.

Edit source on the Mac. Tests, builds, scripts, formatters, package managers,
network access and host operations use the default Linux mailbox route.
A remote script must explicitly `cd` to an approved Linux directory when
needed. This mailbox does not provision a remote checkout or special hardware,
and the Mac source path must not be assumed to exist on Linux.

For project validation, follow [source delivery](source-delivery.md). The
current route supplies a fresh Linux session workspace, not a synchronized
project checkout. Small reviewed source snapshots can be staged and tested
there within one request; record the actual working directory and hashes.

### Reads and review

An allowlisted Mac read may use one literal command through shell-text
`exec_command`, with the checkout in `workdir` and `login: false` when available.
Do not add explicit shell wrappers, pipes, redirection, operators,
substitutions, globs or a second command. `git remote get-url {{GIT_REMOTE}}`
and `shasum -a 256 -- <explicit non-secret source file>` are allowed reads.
Remote/configuration writes remain restricted by `AGENTS.md`.

The working agent performs routine review: check the exact script, files,
scope and effects against the owner task and `AGENTS.md`; for Git, check the
diff/staged list, branch and remote too. Record a short review summary. A
separate human approval is needed only where an existing protected-action rule
requires it. Maintainer-only configuration, service and recovery work remains
separate; calling a request reviewed does not expand its authority.

## 1. Publish a bounded Linux request

Use native workspace file editing. Do not publish using a shell, redirection,
a publisher script, SSH, a direct Runner API or SQLite. Choose a fresh safe
request ID and idempotency key for each new operation; replace `UNIQUE` below.
Keep the request ID consistent with both filenames.

Create `{{MAILBOX_ROOT}}/inbox/req-{{PROJECT_NAME}}-identity-UNIQUE.json`:

```json
{
  "request_id": "req-{{PROJECT_NAME}}-identity-UNIQUE",
  "idempotency_key": "{{PROJECT_NAME}}-identity-UNIQUE",
  "operation": "run",
  "repository_alias": "{{PROJECT_NAME}}",
  "script": "set -euo pipefail\nprintf 'MAILBOX_IDENTITY_OK\\n'\nid -un\nhostname\nuname -m\n",
  "timeout_seconds": 15
}
```

This read-only identity example runs on **the configured Linux worker**. It
does not build or test the project. For actual work, replace the script with
the authorized, bounded, non-interactive operation. Mailbox v1 accepts one
`script` string, not `command`, `argv`, `args` or a `cwd` field. A timeout is a
positive integer in seconds, not a guarantee of successful teardown.

Publish in this order:

1. Create the complete JSON as an owner-owned regular non-symlink file,
   preferably `0644` (native `0600` is also accepted).
2. Read and parse it; check the identity, script, timeout and selection fields.
3. Create an empty regular `0644` (or native `0600`) file named
   `{{MAILBOX_ROOT}}/inbox/req-{{PROJECT_NAME}}-identity-UNIQUE.ready` **last**.
4. Never edit either published file or restore a consumed marker.

The marker is `<request_id>.ready`, **not** `<request_id>.json.ready`. An
accepted input pair may disappear; that does not prove command success.
Keep the request ID, idempotency key and purpose in your task record.
Never put secrets, tokens, certificate/key contents or credential-bearing URLs
in requests or expected output. Use only approved credential references.

### If the editor creates different permissions

Runner accepts exact `0600` or `0644` for owner-owned regular input JSON and
markers; native `0600` is already valid. Use `stat` on the exact path to inspect
it. For another mode on an unpublished JSON draft, use the native editor's
permission setting **before** creating its marker. If the editor has no mode
control, give the maintainer the exact draft path and do not publish.

If a marker already exists or was created with an invalid mode, preserve the
pair and ask the maintainer to inspect its lifecycle before any repair. Do not
chmod, replace, restore or recreate that pair to activate it. Do not run a
shell permission-repair script or submit another request to fix publication.
This exception for safe input modes never permits relaxing mailbox directory
or Runner-produced result/event/diagnostic permissions.

## 2. Mac-only example: push an already reviewed commit

Use this only when the task authorizes the push, the intended commit is
reviewed, and the actual branch and remote were checked. It executes on the
**Mac**, as the mailbox's configured owner, in the canonical source checkout.
It is not a general-purpose Mac execution route.

Create a fresh JSON directly under `{{MAILBOX_ROOT}}/inbox/`, then validate it
and publish its matching empty marker last, exactly as in example 1:

```json
{
  "request_id": "req-{{PROJECT_NAME}}-git-push-UNIQUE",
  "idempotency_key": "{{PROJECT_NAME}}-git-push-UNIQUE",
  "operation": "run",
  "repository_alias": "{{PROJECT_NAME}}",
  "environment": "mac-dev",
  "execution_target": {
    "kind": "local",
    "profile": "mac-workstation"
  },
  "script": "set -euo pipefail\ncd '{{PROJECT_ROOT}}'\ngit -c core.sshCommand='ssh -i \"{{GIT_SSH_IDENTITY_FILE}}\" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push '{{GIT_REMOTE}}' '{{GIT_BRANCH}}'\n",
  "timeout_seconds": 60
}
```

The context name `mac-local` is not a wire environment value: both selection
fields above are required together. Use only this project's verified pair and
dedicated SSH identity; Runner does not inherit an interactive SSH agent.
Reviewed `git add -- <explicit files>` and `git commit` use the same Mac pair.
Review the exact diff/staged scope first, preserve unrelated changes and never
use broad staging, force-push, reset, rebase, checkout or remote Linux Git
mutations. The project policy is stricter than Runner's target permission;
being able to select Mac-local does not authorize arbitrary Mac scripts.

## 3. Follow this exact request to its result

For each published request, read:

```text
{{MAILBOX_ROOT}}/outbox/<request_id>.json
    -> command_id from that response
{{MAILBOX_ROOT}}/events/<command_id>.ndjson
```

Observe the same outbox as revisions change. `request_state: accepted` is
nonterminal, including queued, running, session creation and uncertain
delivery. A missing event cursor is not proof that nothing executed.
Terminal request states are `complete`, `rejected` and `indeterminate`;
`complete` alone does not mean success.

For executed work, success requires all applicable evidence to agree:

- `command_state: succeeded` and `exit_code: 0`;
- the expected output, `output_complete: true`, `output_truncated: false`;
- a finished delivery and `teardown_outcome: closed`;
- the event prefix through the response's `available_event_sequence`, whose
  final event agrees with the command result.

Use the latest response revision and advertised cursor. A failed command can
still have complete output and closed teardown: preserve its actual nonzero
exit code and failure. A pre-execution terminal failure may have no command,
events, exit code or cursor; do not invent them. `indeterminate`, incomplete
output or LOST/unconfirmed teardown require investigation, not a success claim.

No outbox? Read `{{MAILBOX_ROOT}}/diagnostics/<request_id>.json` for a safe
ingress rejection. If neither exists, preserve the request and ask the Runner
maintainer to inspect its supported lifecycle status. Do not query SQLite,
restart Runner, remove locks or resend the work to obtain an answer.

## 4. ACK the preserved terminal response

First preserve the terminal response and advertised events needed for the
task. Verify terminality and the actual evidence, even when the outcome is
failure or uncertainty. Then create `{{MAILBOX_ROOT}}/acks/<request_id>.json`
with the exact request ID, latest terminal `response_revision` and advertised cursor.
For example, **only if the actual response advertises revision 2 and cursor 5**:

```json
{
  "request_id": "req-{{PROJECT_NAME}}-identity-UNIQUE",
  "response_revision": 2,
  "available_event_sequence": 5
}
```

If a terminal response has no `available_event_sequence`, omit that optional
field. For example, **only if its actual terminal revision is 2**:

```json
{
  "request_id": "req-{{PROJECT_NAME}}-pre-execution-failure-UNIQUE",
  "response_revision": 2
}
```

Do not substitute cursor zero or copy example numbers into an ACK without
checking the actual response. Read/validate the regular owner-owned `0644`
(or native `0600`) ACK JSON, then create its empty regular `0644` (or native
`0600`) `{{MAILBOX_ROOT}}/acks/<request_id>.ready` marker **last**.
Never ACK an `accepted`/nonterminal response or an ingress diagnostic.

**Terminal `indeterminate` is ACK-eligible.** Preserve its exact terminal
response and any advertised event prefix first, then ACK that response's
revision/cursor using the same rules. This acknowledges receipt of an unknown
outcome; it does not establish successful execution, exit, teardown or free
capacity. Keep the unresolved mutation/cleanup in your task record and obtain
a supported diagnosis. ACK never authorizes replay. A merely uncertain
`accepted` request, missing terminal response or unavailable advertised events
is **not** an ACK candidate; preserve it for the maintainer.

Runner may consume valid ACK files. Their disappearance alone is not durable
ACK proof; the maintainer can confirm `terminal_acknowledged` through the
supported read-only lifecycle query. ACKs acknowledge receipt, not success,
process exit or authorization to release capacity. Do not overwrite an
existing ACK draft or marker: ask the maintainer to reconcile that exact pair.

## 5. When work is uncertain

| Observation | What to do |
| --- | --- |
| Accepted/queued with no terminal cursor | Keep observing the same request; record any job, session and command IDs. |
| `queue_blocked_reason: lost_capacity_recovery_pending` | Preserve the exchange and ask the maintainer; this is nonterminal capacity evidence, not permission to recover. |
| Terminal failure/rejection | Preserve the reason/result. Correct a separately authorized new operation using a new request ID and key. |
| Unknown result of a mutation | Do not replay, replace, redispatch or change its identity. Obtain a supported diagnosis. ACK is allowed only if an actual terminal response and its advertised events have been preserved. |
| LOST teardown, retained capacity or unreachable result | Ask the maintainer for scoped recovery; do not cancel, signal processes, release capacity or restart services. |
| Private output cannot be read | Ask for an appropriately scoped read capability or operator evidence; do not weaken permissions. |

Mailbox directories remain owner-only `0700`; Runner-produced results,
events and diagnostics remain private `0600`. The reference documentation
does not change those access boundaries. Never ACK nonterminal work, restore
request markers, or treat missing files as proof of execution or cleanup.
