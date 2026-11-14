# Set up Runner for a new project

Use this guide to give a project its own mailbox on the **existing Linux worker**.
It does not provision a machine or deploy a new Runner release.

| Work | Where it happens |
| --- | --- |
| Read/inspect files and edit source or documentation | Canonical Mac checkout |
| Reviewed `git add -- <explicit files>`, `git commit`, `git push` | Mac, through the project's mailbox |
| Tests, builds, scripts, formatters, package managers, network and service operations | Linux, through the project's mailbox |

The Git-only Mac restriction is project operator policy. Runner's `mac-local`
permission itself is not a command filter or a sandbox.

## 1. Choose the project and route

For a project named `my-project`, use:

| Setting | Example |
| --- | --- |
| Canonical checkout: `{{PROJECT_ROOT}}` | `/Users/tomasz.walczuk/projects/my-project` |
| Mailbox ID and repository alias: `{{PROJECT_NAME}}` | `my-project` |
| Mailbox root: `{{MAILBOX_ROOT}}` | `/Users/tomasz.walczuk/projects/my-project/runner/mailbox-` |
| Default execution context: `{{LINUX_CONTEXT}}` | `ubuntu-current` |
| Allowed contexts | `ubuntu-current`, `mac-local` |
| Remote profile: `{{LINUX_PROFILE}}` | `linux-host` |
| Worker address: `{{LINUX_ADDRESS}}` | Currently `79.72.94.59`; verify the host profile |

The **Runner maintainer** checks the selected lane's installed configuration and
host profile before using these values. For PRD, these are outside Git:

- Mac: `~/Library/Application Support/RemoteSessionRunner/config/mac.yaml`
- Host profile: `~/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json`

Verify the context's **environment, target profile and address** against the
worker. A legacy environment label such as `sandbox-dev` can name a policy on
`linux-host`; its name does not select the machine or lane. Do not copy an old
`linux-dev` label blindly, rename worker policies, or add duplicate aliases to
make a name look right. Those changes can break historical status reads or
make maintenance probes ambiguous.

## 2. Add the project files

Copy [the complete AGENTS.md template](templates/project-AGENTS.md) into the
project's root as `AGENTS.md`. Replace every `{{...}}` placeholder using step 1
and these Git settings:

| Placeholder | Fill with |
| --- | --- |
| `{{GIT_REMOTE}}` | The reviewed remote name, usually `origin` |
| `{{GIT_BRANCH}}` | The actual branch, such as `main` or `dev` |
| `{{GIT_SSH_IDENTITY_FILE}}` | The approved remote's dedicated SSH key, as an absolute Mac path |

Do not assume GitHub or copy another project's branch/key. Adding `AGENTS.md`
alone does not register a mailbox.

Create `runner/.gitignore` with:

```gitignore
/mailbox-/
```

The project owner prepares the `runner/` parent directory. Its ancestor chain
must contain real directories without group/other write permission, and the
immediate parent must belong to `tomasz.walczuk`. Do not chmod shared ancestors
as a shortcut. **Let Runner create the mailbox root and its children during
activation.** The repository alias is audit metadata; it does not create or
select a Linux checkout. Source editing remains on the Mac.

## 3. Prepare the Runner configuration

**Mac — Runner maintainer, working from the Runner checkout.**

Prepare a separate owner-only `0600` candidate from the **current installed
configuration**, outside Git. Add this entry to its existing `mailboxes` map:

```yaml
    my-project:
        root: "/Users/tomasz.walczuk/projects/my-project/runner/mailbox-"
        repository_aliases: [my-project]
        default_execution: ubuntu-current
        allowed_execution: [ubuntu-current, mac-local]
        durable_orphan_cleanup: false
```

Keep existing mailbox IDs, roots, execution contexts, credentials and routes.
Do not replace the complete configuration with this fragment or an old example.

## 4. Activate through the Runner maintainer

This is a reviewed maintenance procedure, **not a one-command installer**.
Project agents follow their mailbox-only rules; they do not run these Mac
administration commands directly. Reuse applicable owner authorization and bind
the maintenance record to the actual selected release and candidate hash.

1. Confirm publishers are paused and work is drained. Use a fresh supported
   inventory to distinguish executing/queued/resumable work and in-flight
   mutations from retained terminal-lost holds. Preserve old holds, results,
   processes and ACK history; adding a mailbox does not authorize recovery.
2. Prove no admission-holding cohort or temporary ownership handoff conflicts
   with activation. Record the current selector, configuration hash and process
   identities, and check capacity for a consistent DB/configuration/launch
   backup.
3. Run the installed binary's read-only candidate checks:

   ```sh
   # Mac — Runner maintainer; candidate is a reviewed 0600 file outside Git
   root="$HOME/Library/Application Support/RemoteSessionRunner"
   candidate="$root/config/mac.my-project.next.yaml"
   "$root/current/bin/runner-local" validate-config --config "$candidate" \
     --check-mailbox-directories --check-retained-mailboxes
   ```

   Then take and verify the consistent **catalog** backup. `runner-lane` is
   separate operator tooling, **not** a service-bundle binary. Substitute the
   verified tool path and actual selected release:

   ```sh
   # Mac — Runner maintainer; this creates a consistent backup, not a deployment
   runner_lane='/absolute/path/to/verified/runner-lane'
   release='ACTUAL-SELECTED-RELEASE-ID'
   "$runner_lane" catalog-prd-backup --host mac-control-plane --release-id "$release"
   ```

4. For a mailbox-only change with unchanged executor policy, refresh **only**
   `gui/502/com.remote-session-runner.local`. Use `launchctl bootout` for that
   exact label, prove the old ingress process incarnations exited and its API
   socket stopped accepting, and repeat the candidate checks. Keep the
   executor, retained runtime processes and peer lanes unchanged.
5. While ingress is stopped, atomically hand off the reviewed `0600` candidate
   to `config/mac.yaml`, preserving the exact original and backup. Bootstrap
   the existing `~/Library/LaunchAgents/com.remote-session-runner.local.plist`
   into `gui/502`, then kickstart that exact ingress label. Normal startup
   reserves its socket, registers the complete mailbox set durably, then
   creates the root and `inbox`, `outbox`, `events`, `acks`, `diagnostics`.
6. Verify both Mac services' readiness and unchanged release provenance, the
   new lifecycle registry entry, directory ownership/modes and existing-lane
   and retained-work invariants. A healthy API alone does not pass mailbox
   execution acceptance.

After startup may have registered the mailbox, **repair forward with its stanza
retained**. Do not restore an older config or DB to erase partial activation.
Do not hand-call `--activate-mailbox-set`. The legacy
`deploy/macos/install-launchagents.sh` refuses catalog-selected PRD;
`prd-stop`/`prd-start` affect both Mac components and are not this ingress-only
refresh. If the change needs different executor policy, schema, trust or
service bytes, stop and prepare that separate change under its own procedure.

## 5. Prove the mailbox works

**New project's agent — native workspace file editing, after activation.**

Replace `my-project` and `UNIQUE` below. Create the JSON at
`runner/mailbox-/inbox/<request_id>.json`, read/validate it, then create the
empty matching `<request_id>.ready` **last**. Both files must be regular,
owner-owned and exact `0644`. Omit both selection fields to test the default:

```json
{
  "request_id": "req-my-project-onboarding-UNIQUE",
  "idempotency_key": "my-project-onboarding-UNIQUE",
  "operation": "run",
  "repository_alias": "my-project",
  "script": "set -euo pipefail\nprintf 'RUNNER_ONBOARDING_OK\\n'\nid -un\nhostname\nuname -m\n",
  "timeout_seconds": 15
}
```

Read the matching outbox until terminal, then its advertised event prefix.
Pass requires the expected `ubuntu` account and **actual Linux hostname and
architecture**, exit `0`, complete untruncated output and closed teardown.
Preserve IDs and investigate a rejection or uncertain result; do not resubmit
or restore markers. Read `diagnostics/<request_id>.json` if no response exists.

Only after verification, publish its exact terminal ACK JSON and empty marker
last. Use the response's revision and advertised `available_event_sequence`;
if no cursor was advertised, omit that optional field rather than inventing one.
Confirm `terminal_acknowledged` through the maintainer's supported lifecycle
query. Root/children stay `0700`; Runner outputs/events/diagnostics stay `0600`.
See [the mailbox protocol](mailbox.md) for full schemas and failure handling.

Record the route, accepted release, config/backup hashes, test IDs, result,
cursor and ACK proof. Linux acceptance does not prove the optional Mac Git
execution path; report that separately. The project is ready when these checks
pass; external clients are resumed by their owner.
