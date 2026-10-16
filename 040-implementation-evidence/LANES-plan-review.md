# Lane plan — implementer-comment resolution

**Status:** documentation validation and source delivery PASS. This is a
plan amendment, not execution or acceptance of L0 or any later lane milestone.

## Scope and baseline

- Date: 2026-10-16 (Europe/Warsaw).
- Authoritative source: Mac, `tomasz.walczuk`,
  `/Users/tomasz.walczuk/projects/remote-session-runner`, branch `dev`.
- Pre-change HEAD: `e73c22e37445ea57698bcc01b15af7aedba9774f`; worktree clean.
- Inputs: repository instructions and LLM client guide; original phased-plan
  operating protocol; current lane idea, design/plan and relevant acceptance
  contracts; P165 source/evidence-closeout precedent; BUG-021–024 and register.

## Implementer comments and resulting contracts

1. **Precedence and phase evidence:** the lane plan now explicitly specializes
   the historical P001–P148 process without reopening/renumbering P evidence.
   It requires a separate `LANES-000-execution-ledger.md`, bounded serial units,
   `LANES-*` evidence, inherited test/stop/durability safeguards, source revision
   S delivery before host validation, then separately delivered evidence-only
   closeout E. The next preflight/final handoff records E without self-SHA
   recursion. AGENTS and the original plan link this specialization and name
   all three required Ubuntu profiles.
2. **BUG-021–024 mapping:** the design defines per-route serving-build and
   capability triage, mandatory timeout/explicit-limit/managed-session positive
   gates for each remote lane/profile, and retained PRD Mac-local teardown and
   capacity-release proof. Dev/QAS local execution remains forbidden. L0 only
   inventories; actual repairs and historical retained-work disposition remain
   separate work. No serving revision, bug closure, original ACK, or recovery
   is inferred from this amendment.
3. **Approval ownership:** Tomasz Walczuk is accountable for arranging approvals;
   the actual approver, executing operator, delegation and verified access must
   be recorded. The table names evidence/deadlines for management backup
   storage, Dev/QAS storage, bootstrap/PKI, cloud/host networking, and mailbox
   producers/PRD maintenance. Pending records grant no live authority and cannot
   replace signer mandates or expand renewal grants.

## Validation on the Mac

| Check | Result |
| --- | --- |
| `git diff --check` | PASS |
| Read-only Python check of Markdown fences, final newlines and local links/anchors in the four changed documents | PASS: 11 local links checked; fence counts 2 / 0 / 36 / 0 |
| Independent review of phase precedence, bounded units and S/E evidence ordering | PASS |
| Independent review of bug applicability, required positive capabilities and retained-work safety | PASS |
| Independent review of external owners, handoffs and approval boundaries | PASS |
| Go/unit/race and installed/live suites | Not applicable to this documentation-only amendment; no executable changes or runtime acceptance claim |

Source reading/editing and Mac validation/Git handoff use the session's existing
Mac-shell authorization. Only source-checkout synchronization is part of
delivery. No service installation/restart, mailbox publication, ACK, bug
reproduction, storage, firewall, credential or runtime-configuration change was
performed. The BUG-024 original retained identities and held ACK are untouched.

## Source and evidence delivery

Source revision S is `822c7c3e9661368d966ef081de948b896713093f`
(`docs(lanes): clarify implementation process and acceptance gates`). The
following source handoff checks all passed with exit code 0:

| Location | Verified source revision and condition |
| --- | --- |
| Mac `dev` | S; clean after source commit |
| Gitea `origin/dev` | S; primary push advanced from `e73c22e` |
| Automatic GitHub `github/dev` | S; exact mirror equality before host pulls |
| Ubuntu `linux-host` | Clean `dev`/expected GitHub origin; fast-forwarded `e73c22e → S`; resulting HEAD S, clean |
| Ubuntu `sandbox-host` | Clean `dev`/expected GitHub origin; fast-forwarded `e73c22e → S`; resulting HEAD S, clean |
| Ubuntu `factory-sandbox` | Clean `dev`/expected GitHub origin; fast-forwarded `e73c22e → S`; resulting HEAD S, clean |

On the **Mac**, in the authoritative checkout:

```sh
git diff --check
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev
```

On **each named Ubuntu host**, in
`/home/ubuntu/projects/remote-session-runner`, after checking branch, clean
worktree, pre-pull HEAD and expected GitHub origin:

```sh
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
git rev-parse HEAD
git status --porcelain=v1
```

The Mac initiated only these source-checkout operations with pinned SSH host
verification, `IdentitiesOnly=yes`, `BatchMode=yes`, `UpdateHostKeys=no`, and
explicit identities: `remote-session-runner` alias/key for `linux-host`,
`sandbox.env` with `~/.ssh/dev.slidestud.io` for `sandbox-host`, and
`factory-sandbox` alias/key for that host. No installed build identity is
inferred from the resulting checkout HEADs.

This evidence-only closeout E must follow the same primary/mirror/three-host
delivery. Its own SHA and final equal clean HEAD checks belong in the final
post-commit handoff; source checks need no rerun merely for this evidence prose.
No lane execution ledger or L0 PASS is created by this review.
