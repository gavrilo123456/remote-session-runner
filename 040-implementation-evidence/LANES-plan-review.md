# Lane plan — implementer-comment resolution

**Status:** documentation validation PASS; source delivery pending. This is a
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

Pending source commit, authoritative Gitea push, automatic GitHub mirror
verification and clean `dev` fast-forward/equal-HEAD checks on `linux-host`,
`sandbox-host`, and `factory-sandbox`. Record the source SHA and actual results
in an evidence-only closeout; report that closeout SHA after its own verified
delivery. No lane execution ledger or L0 PASS is created by this review.
