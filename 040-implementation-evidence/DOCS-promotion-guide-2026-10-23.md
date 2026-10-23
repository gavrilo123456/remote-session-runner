# Documentation: simple Dev → QAS → PRD promotion guide

Owner request: provide compact, plain-language instructions explaining how a
Runner code change reaches production.

## Delivered content

- Added [the promotion guide](../docs/promotion-guide.md), covering source
  delivery, one frozen four-host release, Dev/QAS acceptance, scoped PRD
  approval, deployment, reopening and compatible rollback.
- Distinguished release promotion from mailbox ownership transfer and the
  one-time October migration exception.
- Linked the guide from both README indexes and the lane runbook.
- Corrected the lane runbook's obsolete CAT-not-started and legacy-only PRD
  descriptions; retained the distinct guards for legacy operations.

## Validation

- `git diff --check` and `git diff --cached --check`: PASS.
- Relative-link check across the four documentation files: PASS, 40 targets.
- Read-only review against `AGENTS.md` and release-design sections 5–6: PASS.
- Runtime tests: NOT RUN; the change affects documentation only.

## Verified content delivery

Content commit: `549e00bc0bc6a53d224f78038df1d78403a88218`.
Starting commit: `1ce61bd952d2f23f002b392a58e52216d73010a0`.

| Location | Verified result |
| --- | --- |
| Mac `dev` | Content committed at `549e00bc0bc6a53d224f78038df1d78403a88218`. |
| Primary Gitea `origin/dev` | Push completed successfully at that commit. |
| Automatic GitHub `dev` mirror | Exact content SHA verified using `ls-remote` before worker pulls. |
| `linux-host` | Clean `dev`; `pull --ff-only` completed; exact content SHA verified. |
| `sandbox-host` | Clean `dev`; `pull --ff-only` completed; exact content SHA verified. |
| `factory-sandbox` | Clean `dev`; `pull --ff-only` completed; exact content SHA verified. |

Git operations used the explicit identities required by `AGENTS.md`. This
records delivery of the content commit; the evidence closeout follows the
same primary → mirror → clean worker fast-forward workflow.

No installed services, releases, selectors, receipts, configuration, databases,
mailbox requests or ACKs changed. Unrelated untracked implementation helpers
were preserved and excluded from these documentation commits.
