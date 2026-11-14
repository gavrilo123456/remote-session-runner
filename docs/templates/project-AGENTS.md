<!-- Copy to the project's AGENTS.md and replace every {{...}} token.
PROJECT_NAME is the configured mailbox ID/repository alias, such as my-project.
PROJECT_ROOT, MAILBOX_ROOT and GIT_SSH_IDENTITY_FILE are absolute Mac paths.
GIT_REMOTE is the reviewed remote name; GIT_BRANCH is the actual branch.
Confirm the complete Mac execution pair below against the selected Runner config.
-->

# Repository workflow for {{PROJECT_NAME}}

## Scope

These rules apply to `{{PROJECT_ROOT}}`.
The Mac checkout is the canonical source-editing workspace. The configured
Remote Session Runner mailbox is the only route for executable work,
network access, host operations and Git writes.

## Direct Mac workspace activity

The following read-only programs may run directly in the canonical checkout:

| Need | Allowed action |
| --- | --- |
| Discover and read code | `pwd`, `ls`, `rg --files`, `rg`, print-only `sed`, `cat`, `nl -ba`, `head -n`, `tail -n`, `wc -l` |
| Inspect metadata | `stat` on an explicit non-secret path |
| Inspect Git | `git status`, `git diff`, `git diff --stat`, `git diff --name-only`, `git diff --name-status`, `git diff --check`, `git log`, `git show`, `git show --check`, `git rev-parse`, `git merge-base --is-ancestor`, `git ls-files`, `git check-ignore -v`, `git worktree list` |
| Inspect JSON | Read-only `jq` |

Source and documentation edits may use native `apply_patch` or a local editor.
The program allowances above never permit secret or token reads.
Use `sed` only for printing; never use `-i`, `e`, `r`, `w` or a sourced script.
For `jq`, do not use filter files, `input`, `inputs`, `--rawfile` or other
options that read another file.

Invoke allowed read-only programs directly through the harness, with arguments
supplied directly. Do not wrap them in `sh`, `bash`, `zsh`, a shell `-c`,
a shell function or a helper script. Shell composition is executable work
and must use the mailbox.

Do not directly run tests, builds, formatters, package managers, scripts,
network clients, SSH, service/deployment/cleanup commands or Git mutations
in the Mac shell.

## File-only execution mailbox

Mailbox root: `{{MAILBOX_ROOT}}`.
Mailbox ID and repository alias: `{{PROJECT_NAME}}`.

Normal requests omit both `environment` and `execution_target`. They use:

```text
{{LINUX_CONTEXT}} -> remote/{{LINUX_PROFILE}} ({{LINUX_ADDRESS}})
```

Execution runs as `ubuntu` on that accepted Linux worker. Do not select
Sandbox, Factory, another profile or an unconfigured execution context.
Use the configured context; an environment label alone does not identify
a machine or lane.

`repository_alias: "{{PROJECT_NAME}}"` is audit and policy metadata.
It does not select a checkout, host or working directory. A required `cd`
belongs inside the bounded script and must name an approved Linux path.
Never assume the Mac project path exists on Linux.

### Mac-local exception: reviewed Git writes only

`mac-local` is allowed only for a reviewed request performing these Git writes
in the canonical Mac checkout:

```text
git add -- <explicit reviewed file paths>
git commit
git push
```

That request must select the complete configured Mac pair. For the current
PRD authority the pair is:

```json
{
  "environment": "mac-dev",
  "execution_target": {
    "kind": "local",
    "profile": "mac-workstation"
  }
}
```

Its script must use the canonical `{{PROJECT_ROOT}}` checkout. Never use
`git add .`, `git add -A`, wildcards, broad directory paths, reset, rebase,
checkout, force-push or Git mutations on the remote Linux worker.

Runner accepts the complete allowed execution pair. The Git-only restriction
is mandatory operator policy; allowing `mac-local` does not enforce that
restriction inside arbitrary scripts.

Use the actual reviewed remote `{{GIT_REMOTE}}` and branch `{{GIT_BRANCH}}`.
Do not assume GitHub, Gitea, `dev` or `main`, create a branch, or change remotes
from an example. Runner does not inherit an interactive SSH agent. For the
approved SSH remote, use its dedicated identity explicitly:

```sh
# Mac execution through the reviewed mailbox Git-only override
git -c core.sshCommand='ssh -i "{{GIT_SSH_IDENTITY_FILE}}" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push "{{GIT_REMOTE}}" "{{GIT_BRANCH}}"
```

Keep private-key contents, credentials and tokens out of requests and output.

## Publish requests and acknowledge results

Use native workspace file editing only. Do not use shell redirection,
publisher scripts, SSH, a direct Runner API or SQLite to publish or reconcile
project work. Hand an inaccessible private result or blocked exchange to the
Runner maintainer; never weaken permissions or remove Runner lock files.

For each new operation:

1. Create complete JSON at `{{MAILBOX_ROOT}}/inbox/<request_id>.json`
   as an owner-owned, regular non-symlink file at exact `0644`.
2. Read and validate it before publication.
3. Create the empty `{{MAILBOX_ROOT}}/inbox/<request_id>.ready`
   as an owner-owned regular `0644` file **last**.
4. Never change either request file after the marker exists.

Use new safe request IDs and idempotency keys for new operations. Scripts
must be bounded and non-interactive; use `set -euo pipefail` where appropriate.
The configured mailbox root and its five children are owner-only `0700`.
Runner-produced responses, events and diagnostics remain private `0600`.

Correlate the existing exchange through:

```text
request_id
-> {{MAILBOX_ROOT}}/outbox/<request_id>.json
-> command_id
-> {{MAILBOX_ROOT}}/events/<command_id>.ndjson
```

Wait for a terminal response. For executed work, inspect the command result,
exit code, complete untruncated output, teardown outcome and the event prefix
through `available_event_sequence`. Report success only when those agree.
If no outbox exists, inspect `{{MAILBOX_ROOT}}/diagnostics/<request_id>.json`.
A pre-execution failure may have no command events; preserve its terminal
failure and reason rather than inventing an exit code or cursor.

After a terminal response, create `{{MAILBOX_ROOT}}/acks/<request_id>.json`
with its exact `request_id`, `response_revision` and advertised
`available_event_sequence`. Omit the optional cursor field if the terminal
response does not advertise one; do not invent zero. Read and validate the
ACK, then create its empty matching `0644` `.ready` marker last.
Do not ACK an ingress diagnostic or an `accepted`/nonterminal response.

Preserve an accepted or uncertain request's identity. Do not duplicate,
replay, redispatch, restore markers, cancel, release capacity or restart Runner
to make progress without a separately supported and authorized procedure.

## Source and remote-host boundary

Edit source and documentation only in `{{PROJECT_ROOT}}` on the Mac.
Validate, build, test, package, deploy and operate the approved Linux
environment through the default mailbox route. Remote commands must not
directly edit tracked source in a remote checkout or make it the canonical
source tree. Do not assume this mailbox provisions a remote checkout,
dependencies, credentials or special hardware.
