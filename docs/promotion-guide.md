# From a code change to production

**Normal flow: change code → build one release → Dev → QAS → approve → PRD.**

Dev is for changing code, QAS tests a candidate release, and PRD serves normal
users. All three run on the existing Mac and three Ubuntu workers. Dev uses
`-dev`, QAS uses `-qas`, and PRD has no suffix.

## The five steps

| Step | What happens | Ready to continue when… |
| --- | --- | --- |
| **1. Share the code** | Edit in the Mac checkout. Commit to Gitea `dev`, verify its automatic GitHub mirror, and fast-forward the three clean Ubuntu checkouts. | Required source tests pass on the intended commit. |
| **2. Build one release** | Freeze the commit. Build one bundle per host, record checksums, and publish the release under `~/projects-runtime/runner-releases/` on each host. | All four bundles share one release ID and are verified. Publishing does not activate them. |
| **3. Test in Dev** | Select the release across all four Dev hosts. Test commands, output, cleanup, limits and routes. | Dev acceptance passes. PRD stays unchanged. |
| **4. Test in QAS** | Select the **same release and unchanged host bundles**. Test representative workflows and compatible rollback. | QAS acceptance passes; the PRD plan is ready for your approval. |
| **5. Approve and deploy PRD** | Approve that release and maintenance plan. The operator updates Linux → Sandbox → Factory → Mac. | All four hosts pass functional checks, deployment is complete, and normal admissions are reopened. Clients can resume. |

Before updating a lane, the operator pauses its affected clients, settles
existing work/results, takes consistent backups and checks space and database
compatibility. New work stays held until that lane passes its checks.

The implementer records the release ID, tests and rollback information.
**“Ready” alone does not prove commands work.**

## What stays separate

- **Git and running services:** pushing to `dev` shares source. Promotion
  selects built releases, without merging into QAS/PRD branches.
- **Each lane's state:** configuration, databases, credentials and mailboxes
  stay separate. Do not copy Dev/QAS state into PRD.
- **Mailbox ownership:** routine releases keep clients on their existing
  lane. Moving mailboxes is a separate [handoff](mailbox-handoff.md).

## If something fails

Stop promotion and use supported recovery or compatible rollback. Preserve
existing work. Old code must support the current database and new records;
rollback does not automatically restore an old database.

Changed code needs a new release and renewed Dev → QAS testing. Never rebuild
a tested release under the same ID.

## A message to start the process

> Prepare a release for **[change or commit]**. Complete Dev acceptance, then
> promote the same release to QAS. After QAS passes, present the PRD deployment
> and rollback plan for my approval. Keep PRD running during Dev/QAS testing.

The October 2026 Dev → PRD → QAS exception was specific to that migration.
**New releases follow Dev → QAS → PRD.**

Operator details: [release design, sections 5–6](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md#5-promotion-and-selected-lane-upgrade),
[CAT plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md),
and [Git workflow](../AGENTS.md#commit-to-primary-verify-the-mirror-then-fast-forward-the-test-hosts).
