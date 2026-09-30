# P158 — SlideStudio external mailbox

**Status:** IN PROGRESS. This phase adds and accepts one configured external
mailbox. It does not onboard a new Ubuntu host and does not change the
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

The source implementation now awaits its Mac commit, GitHub `dev` push, and
clean fast-forward handoff to both Ubuntu checkouts. The Mac candidate
activation, native mailbox acceptance, exact ACK, sandbox P128 status, and
final documentation/evidence closeout remain pending. The known sandbox root
filesystem free-space margin must be rechecked before its host gate; it is a
capacity risk, not a passed or failed P158 gate.
