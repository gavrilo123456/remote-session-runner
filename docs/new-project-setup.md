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
The working project agent performs routine review of the exact request, scope,
files and effects against the owner task and project policy. Git review also
covers staged paths, branch and remote. It records a short review summary;
this is not a new human-approval gate. Existing protected-action approvals
still apply. The Runner maintainer owns configuration/service/recovery changes
and publication-permission exceptions.

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

### Choose source delivery and the Linux working directory

A mailbox route does **not** synchronize the Mac checkout. Record this part of
onboarding separately from executor readiness. For small non-secret source
sets on the current `empty`-source route, use the
[source-snapshot procedure](templates/mailbox-docs/source-delivery.md): the
project agent reads and SHA-256-hashes the exact approved Mac files, embeds a
reviewed manifest in one bounded `run.script`, then verifies and stages those
bytes under a fresh `source/` child of Runner's Linux session workspace.
Tests run from that child in the **same request**. Record the actual `pwd`,
file hashes and command outcome. Source editing remains on the Mac; creating
this disposable, untracked snapshot is permitted remote execution, not editing
a Linux Git checkout.

There is no implicit `/home/ubuntu/projects/my-project` working tree. Each
session gets its own workspace, removed after confirmed closed teardown.
`repository_alias` does not change that. Keep secrets and unreviewed files out
of snapshots; include the complete source/build inputs needed for the test.
The decoded script limit is 128 KiB and serialized request limit is 1 MiB.
An identity probe alone never proves source delivery or project tests.

For a larger project, record a separately supported transfer, immutable source
identity and working-directory procedure before declaring Linux source
validation ready. Do not guess a mirror, authorize remote Git mutations by
implication, or assume `git_revision` is enabled: the current live route and
production wiring do not provide that automatic source-delivery path.

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

Also render both files in
[the mailbox handbook template](templates/mailbox-docs/README.md), including
[source delivery](templates/mailbox-docs/source-delivery.md), with the same
reviewed values. Keep these staged copies outside the live mailbox until step 4.
Every new project receives the handbook at
`{{MAILBOX_ROOT}}/docs/README.md` and its adjacent `source-delivery.md`;
its `AGENTS.md` links to both. For the standard layout the handbook is at
`runner/mailbox-/docs/README.md`. Adjust the
relative link for a different approved root. The agent reads this handbook on
first use or when it needs detail, rather than on every request.

The template explicitly permits single literal allowlisted reads through a
shell-text `exec_command` tool, using `workdir` and `login: false` when
available. This covers tools without an argument-array interface; it does not
permit shell composition. It also names `git remote get-url` for remote checks
and `shasum -a 256 -- <explicit non-secret source file>` for snapshot hashes.

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

The versioned templates in this Runner repository are the maintained source.
The deployed handbook lives inside ignored mailbox runtime data; do not commit
the mailbox tree, responses or ACKs to the project. Record the template revision
and rendered file hash in the setup evidence. Regenerate and review a copy when
the protocol or project route changes; do not silently overwrite custom text.

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
7. Install the reviewed rendered `docs/README.md` and `docs/source-delivery.md`
   as regular owner-owned `0644` files, with a real owner-owned `0700`
   `docs` directory alongside `inbox/`, not inside it.
   Preserve the mailbox root and its five existing directories at `0700`.
   Use no symlinks and publish no `.ready` marker for documentation. Verify
   all placeholders are resolved and the project's `AGENTS.md` links open them.
   A documentation-only update on an already configured mailbox needs no
   service restart, selector change or new request.

After startup may have registered the mailbox, **repair forward with its stanza
retained**. Do not restore an older config or DB to erase partial activation.
Do not hand-call `--activate-mailbox-set`. The legacy
`deploy/macos/install-launchagents.sh` refuses catalog-selected PRD;
`prd-stop`/`prd-start` affect both Mac components and are not this ingress-only
refresh. If the change needs different executor policy, schema, trust or
service bytes, stop and prepare that separate change under its own procedure.

### Documentation during mailbox maintenance

Keep documentation at the mailbox root, **outside `inbox/` and `acks/`**.
Although normal intake ignores a literal `inbox/docs/` folder, retirement and
strict ingress snapshots reject unexpected inbox entries; ownership handoff
also archives and replaces the inbox. The sibling `docs/` layout avoids these
conflicts and never grants new execution permissions. Keep examples as fenced
JSON in Markdown; do not create example request files or `.ready` markers.

Runner does not generate or update this reference directory automatically.
The maintainer installs it as part of every new-project setup. Its source is
the versioned template, so it can be restored without copying runtime work.
Catalog DB/configuration backups do not automatically include external
mailbox documentation; preserve the rendered copy and its hash separately.
After a handoff or route change, verify the handbook still matches the selected
authority and project policy before using it. Do not change mailbox state or
relax maintenance guards to repair documentation.

## 5. Prove the mailbox works

**New project's agent — native workspace file editing, after activation.**

Replace `my-project` and `UNIQUE` below. Create the JSON at
`runner/mailbox-/inbox/<request_id>.json`, read/validate it, then create the
empty matching `<request_id>.ready` **last**. Both files must be regular,
owner-owned and preferably exact `0644`; native-created exact `0600` is also
valid for request/ACK JSON and markers. Other modes require native metadata
correction while still an unpublished draft, or maintainer assistance. Once a
marker exists, preserve the pair and request supported inspection rather than
chmod/replacing it. Never relax result or directory permissions.
Omit both selection fields to test the default:

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
An actual terminal `indeterminate` response may be acknowledged after its
response and any advertised events are preserved; the ACK acknowledges receipt
of uncertainty, not success or cleanup. Keep the unresolved work recorded and
do not replay it. An `accepted`/nonterminal exchange is never ACK-eligible.

Record the route, accepted release, config/backup hashes, test IDs, result,
cursor and ACK proof. Linux acceptance does not prove the optional Mac Git
execution path or project source delivery; report those separately. To declare
source validation ready, also verify the chosen snapshot/transfer with known
Mac bytes, expected hashes, actual Linux working directory and terminal test
result. External clients are resumed by their owner.
