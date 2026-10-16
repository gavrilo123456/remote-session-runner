# Lane priority amendment — finish the current case, then implement lanes

**State:** PASS — documentation review and source S delivery verified below.
The evidence-only closeout must itself be delivered before the final handoff.
No lane unit or runtime acceptance is passed by this record.

## Owner instruction and reason

On 2026-10-16 the owner asked why the implementer was repairing bugs instead
of implementing Dev/QAS/PRD, and directed it to finish only the work already
open, then proceed to the lanes. The former handbook explicitly assigned
BUG-021–024 baseline diagnosis, repairs and live verification before lane
work. This amendment replaces that assignment rather than relying on an
informal instruction that contradicts the checked-in plan.

Pre-amendment Mac `dev` HEAD was
`e1912ac642ce5d819b5cfcce980898d0eba222aa`, with a clean worktree. Read-only
preflight verified all three Ubuntu checkouts clean on `dev` at that SHA with
the expected GitHub origin. Primary and mirror verification and final delivery
are recorded in the closeout below.

## Changed assignment

- Finish the already-open `LANES-B021-02` timeout maintenance/acceptance and
  only the `LANES-B024-02` teardown work directly required by it. The selected
  retained pair remains
  `sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`.
- Defer all four unstarted B022/B023 baseline units. Their reports stay open;
  DEFERRED is neither PASS nor removal of the required candidate behavior.
- Make `LANES-L1-01` the next lane source implementation unit. Independent
  lane source work can proceed while current live maintenance waits on an
  actual publisher pause, access or operational approval. Keep its exact
  blocked evidence and the failed PREP/L0 states visible.
- Preserve mandatory lane tests, positive timeout/explicit-limit/managed
  lifecycle/PRD teardown acceptance, immutable candidate promotion, source
  delivery and all actual host/live-operation dependencies. Correct a failure
  required by an active lane unit within that unit; do not automatically open
  another standalone baseline repair campaign.

The idea, detailed design, phased plan, handbook, ledger, AGENTS and current
handoff/B021 evidence are aligned with that scope. Historical results and
retained-work/ACK facts are preserved. No bug report is marked resolved by
this amendment.

## Operational boundary

This change edits project documentation only. It does not pause publishers,
restart a service, recover capacity, install an artifact, publish a request or
write an ACK. Previous maintenance-window and platform/UI facts are not
reclassified as current approval or pause proof.

The old `e1912ac` candidate package becomes historical when the amended source
HEAD is delivered. Before later maintenance, rebuild and freeze candidates
from the exact clean synchronized revision after documentation closeout,
verify their hashes, and refresh the concrete operational package outside Git.
Keep that revision unchanged during its approved continuous live transaction.

## Validation and delivery

Documentation validation covers diff whitespace, local links/anchors, Markdown
fences, unchanged unit IDs/order, exactly four deferred B022/B023 ledger rows,
and consistency of the bounded closeout and retained candidate acceptance.
Pre-source checks **PASS**: nine Markdown files, 46 local links/anchors,
balanced fences/final newlines, the same 68 unit IDs in the same order in
ledger/handbook, and all four deferred rows. An independent scope review found
the idea/design/plan/handbook/ledger consistent with the owner decision.
Executable suites are not applicable: no source, test, schema or deployment
file changes. Existing test results are not rerun or claimed as live acceptance.

Source revision S and evidence closeout E follow AGENTS: Mac commit, primary
Gitea push, exact automatic GitHub mirror verification, then clean `dev`
fast-forward and exact-HEAD verification on `linux-host`, `sandbox-host`, and
`factory-sandbox`. E records S's completed checks; the final handoff records
E's delivery without a self-referencing commit.

### Verified source S delivery

Source S: `84870ee89d8cfd9ce2ac5d1cb408d6b6ff1367eb`
(`docs(lanes): finish open bug work then prioritize release lanes`). The
intended diff contains exactly the nine Markdown files described above.

| Surface | Verification and result |
| --- | --- |
| Mac `dev` | Intended files staged explicitly; `git diff --cached --check` PASS; source commit S; clean worktree afterward |
| Gitea primary `origin/dev` | Explicit-identity `push origin dev` succeeded; `ls-remote origin refs/heads/dev` returned exact S |
| GitHub automatic mirror `github/dev` | Explicit-identity `ls-remote github refs/heads/dev` returned exact S before any Ubuntu pull |
| `linux-host` | Guarded clean `dev`, expected GitHub origin and previous `e1912ac` HEAD; `pull --ff-only origin dev` exit 0; exact S and clean worktree; native `git diff --check e1912ac642ce5d819b5cfcce980898d0eba222aa 84870ee89d8cfd9ce2ac5d1cb408d6b6ff1367eb` PASS |
| `sandbox-host` | Same clean-branch/origin/previous-HEAD guards; fast-forward exit 0; exact S and clean worktree; same native diff validation PASS |
| `factory-sandbox` | Same clean-branch/origin/previous-HEAD guards; fast-forward exit 0; exact S and clean worktree; same native diff validation PASS |

Mac primary SSH used `/Users/tomasz.walczuk/.ssh/local-gitea-key`; mirror
verification used `/Users/tomasz.walczuk/.ssh/gavrilo123456-github`.
Mac-to-host SSH used aliases `remote-session-runner`, `sandbox.env`, and
`factory-sandbox`, with their explicit identities
`/Users/tomasz.walczuk/.ssh/remote-session-runner`,
`/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, and
`/Users/tomasz.walczuk/.ssh/factory-sandbox`. All used `IdentitiesOnly=yes`,
`BatchMode=yes`, `StrictHostKeyChecking=yes`, and `UpdateHostKeys=no`.
On each Ubuntu host the guarded pull command was:

```sh
# Ubuntu — ubuntu, /home/ubuntu/projects/remote-session-runner
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UpdateHostKeys=no' pull --ff-only origin dev
```

The first sandboxed Mac commit could not create `.git/index.lock`; the
authorized escalated retry succeeded. The first push updated the primary but
could not update the local remote-tracking ref; direct primary/mirror reads
verified S, and an authorized repeated push completed the local bookkeeping.
Initial sandboxed SSH validation connections timed out after the successful
pulls; authorized escalated read-only retries verified all three host diffs.
No failed attempt changed installed services or mailbox state.

This evidence-only closeout E records completed S checks. Deliver E by the same
primary → exact automatic mirror → clean three-host fast-forward sequence;
record E's final SHA in the handoff. No executable-suite rerun is required for
this delivery prose, and no further self-referencing evidence commit is needed.
