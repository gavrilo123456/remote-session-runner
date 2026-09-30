# P158 — SlideStudio external mailbox

**Status:** PASS. This phase added and accepted one configured external mailbox.
It did not onboard a new Ubuntu host and did not change the
software-process-crash-only durability boundary.

## Objective and selected policy

| Item | Selected value |
| --- | --- |
| Mailbox ID and repository alias | `slidestud-io` |
| Mailbox root | `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` |
| Default execution context | `ubuntu-sandbox` |
| Resolved default | `sandbox-dev` / `remote/sandbox-host` |
| Bootstrap alias | `sandbox.env` only; it is not a Runner context name |
| Allowed explicit contexts | `ubuntu-sandbox`, `mac-local`, `ubuntu-current` |

The user clarified that a non-default mailbox may be configured at any safe
absolute location. P158 therefore preserves the default mailbox and the
existing service-root layout, but adds fail-closed support for an external
root. Its immediate parent must already exist and be owned by the selected Mac
user. Every existing ancestor must be a real directory with no group or other
write permission. Runner creates or restricts only the root and its `inbox`,
`outbox`, `events`, and `acks` children to mode `0700`; it never changes a
project ancestor. Duplicate and nested mailbox roots remain invalid.

## Pre-phase state

- **Mac checkout/account:** `/Users/tomasz.walczuk/projects/remote-session-runner`
  as `tomasz.walczuk`.
- **Mac branch / initial pre-phase HEAD:** clean `dev...origin/dev` at
  `0241b26daee3540c20e37aaacf9da33950938cde`
  (`docs(P158): allow owner-safe external mailbox roots`). A subsequent
  activation-order review was corrected and committed as
  `e489300a3ef9844eea38532b62323373e781564b`
  (`docs(P158): preserve durable external mailbox activation`) before source
  implementation continued.
- **Prior host evidence:** P157 is PASS for `sandbox-host`; its restricted
  queued bridge and direct mTLS application route are distinct from this
  mailbox acceptance.
- **Durability boundary:** P143 remains software-process-crash-only. Physical
  power-loss recovery is unverified and is not exercised by P158.

## Fresh-context inputs

Before implementation, the Mac review covered the root `AGENTS.md`, current
Git status/HEAD, current README, preimplementation decisions, P157 evidence,
the original initial design, detailed design, and phased plan, and the
multiple-inbox initial idea, detailed design, and plan. The initial P158
amendment was committed separately as `0241b26`. A review then found that the
first design could create a new external root before it registered the candidate
set. The corrected durable-boundary design was committed as `e489300` before
source implementation resumed.

| File | Blob revision read before P158 |
| --- | --- |
| `020-initial-design/010-remote-session-runner-initial-design.md` | `12440598a0dba018313196cd80c7848284868f28` |
| `030-detailed-design/010-remote-session-runner-detailed-design.md` | `b24f095d62e6000abed690abf125fd60be4eff1c` |
| `030-detailed-phased-implementaion-plan/010-remote-session-runner-detailed-phased-implementaion-plan.md` | `3f7de2b5ed99332636f15b9d947be5d5662e5f53` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/005-initial-idea.md` | `1fc6e1f95c85183b31b84c2814320d3a4ce82e6b` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/010-multiple-inboxes-and-remote-machines-detailed-design.md` | `cb8e04d2aa546940f35870b557bbc01ef1aec4f8` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/015-multiple-inboxes-and-remote-machines-phased-implementation-plan.md` | `bb3cd9fded70d4512ee6820e5e740ace83bc33e7` |
| `040-implementation-evidence/000-preimplementation-decisions.md` | `7c9a9dde397e8b0fee0cd0b21109dfafbd46ccec` |
| `040-implementation-evidence/P157-sandbox-host.md` | `2315b54428f113251f4c9aa51cf98a06ca7f38df` |

The source review covers `src/internal/config/config.go`,
`src/internal/config/v2.go`, `src/internal/config/config_test.go`,
`src/internal/runnerlocal/serve.go`, `src/internal/runnerlocal/p154_test.go`,
`src/internal/runnerlocal/p155_host_test.go`,
`src/internal/runnerlocal/p157_sandbox_host_test.go`,
`src/internal/runnerlocal/external_mailbox_paths.go`,
`src/internal/mailboxclient/client.go`, `deploy/macos/install-launchagents.sh`,
the Makefile, the macOS V2 template, and the affected documentation/runbooks.

## Planned gates

1. Add config validation for a clean absolute external non-default root while
   preserving the exact default and service-root forms.
2. Add descriptor-relative, non-mutating path validation that rejects unsafe,
   missing-parent, or symlinked external paths. At the irreversible boundary,
   register the candidate set before creating only the owner-only mailbox tree.
3. Add `validate-config --check-mailbox-directories` and make the macOS
   installer run it before stopping either LaunchAgent. Its activation command
   repeats validation, records the candidate, then prepares the tree.
4. Add focused config, runtime, installer-ordering, and race assertions; then
   run the full Mac test suite, race tests, vet, installer shell syntax, and
   diff check.
5. Commit on the Mac, push with the dedicated GitHub key, fast-forward both
   clean Ubuntu checkouts, and verify matching revisions before live use.
6. Activate an owner-only Mac candidate, verify the external tree, run the
   permanent `make test-p158-slidestud-mailbox` native-client gate with neither
   selection field, validate the terminal response/events/exact ACK, and run
   the sandbox P128 zero-work status test.
7. Add a local SlideStudio `.git/info/exclude` entry for `/tmp/mailbox-/` so
   runtime artifacts do not appear as SlideStudio source changes. It is local
   checkout hygiene, not a tracked SlideStudio change.

## Result

### Source implementation gates — PASS

On the Mac as `tomasz.walczuk`, using the shared Runner Go cache and selected
Go 1.27.1 toolchain, the corrected source work passed:

| Gate | Result |
| --- | --- |
| Focused P150/P154/P157/P158 configuration and runtime tests | PASS |
| Focused P129/P158 mailbox and runtime regressions | PASS |
| `make test` | PASS |
| `go test -race ./...` | PASS |
| `make vet` | PASS |
| `sh -n deploy/macos/install-launchagents.sh` | PASS |
| `git diff --check` | PASS |

The focused coverage includes configuration rejection, non-mutating descriptor
preflight, durable registration before real tree creation, post-registration
failure retention, direct-start socket conflict, non-mutating Doctor metrics,
installer ordering, and existing P154/P157 regressions. The source review also
confirmed that the permanent native host gate intentionally omits both
selection fields and asserts the `ubuntu-sandbox` default.

### Source delivery — PASS

The reviewed source was committed on the Mac as
`ce74e368f2aa40f9db90d1bb049f96fd533c5753`
(`phase(P158): support safe external mailbox roots`) and pushed to GitHub
`origin/dev` with the prescribed Mac identity. Both clean Ubuntu `dev`
checkouts then fast-forwarded with the prescribed Ubuntu GitHub identity:

| Checkout | Result |
| --- | --- |
| Mac | clean `dev...origin/dev` at `ce74e368f2aa40f9db90d1bb049f96fd533c5753` |
| `linux-host` checkout | clean `dev...origin/dev` at `ce74e368f2aa40f9db90d1bb049f96fd533c5753` |
| `sandbox-host` checkout | clean `dev...origin/dev` at `ce74e368f2aa40f9db90d1bb049f96fd533c5753` |

No project source was copied to or edited directly on either Ubuntu host.

The required Git handoff used the prescribed identities:

```sh
# Mac — tomasz.walczuk
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Each Ubuntu checkout — ubuntu
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

### Candidate activation and Mac readiness — PASS

The active owner-only candidate contained `default`, `analytics`, and
`slidestud-io`, with the selected external root, alias, default
`ubuntu-sandbox`, and allowed contexts `ubuntu-sandbox`, `mac-local`, and
`ubuntu-current`. Its regular-file mode was `0600`. Before activation,
the following non-mutating Mac command returned schema version 2 with all three
inboxes and left the external root absent:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
GOTOOLCHAIN=local \
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" \
run ./src/cmd/runner-local validate-config --check-mailbox-directories --config \
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The installer command below completed successfully. It recorded the candidate
before creating the external tree:

```sh
# Mac — tomasz.walczuk
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
deploy/macos/install-launchagents.sh --config \
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The active `mac.yaml` then contained all three inboxes, and only the external
root plus `inbox`, `outbox`, `events`, and `acks` were created; every one was a
current-user-owned real directory at mode `0700`. The pre-existing parent
remained the current-user-owned non-group/other-writable directory required by
the descriptor preflight. The local SlideStudio checkout now has an untracked
`.git/info/exclude` entry `/tmp/mailbox-/` so those runtime artifacts do not
appear as source changes.

After restart, both Mac LaunchAgents were running. The Mac readiness report was
`ready`, with `remote_router/linux-host=ready`,
`remote_router/sandbox-host=ready`, and zero aggregate/per-inbox mailbox
backlog.

### Native external-mailbox gate — PASS

On the Mac, the following permanent native gate passed:

```sh
make test-p158-slidestud-mailbox
```

Its native `mailboxclient` test published request
`req-p158-18da276d79d6cbc0` without `environment` or
`execution_target`. It resolved via `inbox_default` to `sandbox-dev` /
`remote/sandbox-host`, returned complete untruncated output containing
`SLIDESTUD_MAILBOX_DEFAULT_SANDBOX_OK`, `ubuntu`,
`oracle-gustaw-janecki-ubuntu-flex-02`, and `aarch64`, read the retained events
through the final cursor, and accepted the exact ACK. The command ID was
`cmd-926a9fe479ededa25e068d4c70cde96c`.

This is a file-only mailbox proof through the permanent restricted bridge. The
separate direct mTLS route was not used as mailbox evidence.

### Sandbox post-request status — PASS

On `sandbox-host` as `ubuntu`, from the clean synchronized checkout, the
following passed:

```sh
cd /home/ubuntu/projects/remote-session-runner && make test-p128-host-status
```

It reported:

```text
active_sessions=0
running_commands=0
unreleased_slots=0
unfinished_jobs=0
```

The post-gate root filesystem margin was `1,383,612 KiB` free (98% used). This
is a future operational capacity risk to monitor; it did not cause a P158 gate
failure.

P158 therefore passes its mailbox-ingress scope. It does not make any other
host accepted, and it does not alter P143: physical power-loss recovery remains
unverified pending a coordinated physical power-cut test.
