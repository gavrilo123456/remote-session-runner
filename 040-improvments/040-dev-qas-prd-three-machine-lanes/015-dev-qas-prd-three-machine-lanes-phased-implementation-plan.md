# Phased implementation plan: Dev, QAS, and PRD three-machine deployment lanes

**Status:** placeholder — do not begin implementation.

## Settled prerequisite for every future phase

The current unsuffixed installation is one PoC runtime. It is the compatibility
baseline for the future PRD mapping, which retains the names `runner-local`,
`runner-locald`, and `runnerd.service`, its existing runtime roots, and direct
HTTPS port `8443`. New concurrent instances would be named `*-dev` for Dev
and `*-qas` for QAS; their proposed Linux ports are `8444` and `8445`
respectively. The future PRD mapping is identified in lane metadata and status,
never by a `-prd` service or path suffix. No lane metadata or suffixed service
is implemented today.

No phase may begin by renaming, stopping, or retargeting the current
unsuffixed installation. It must first create and validate isolated Dev or QAS
resources under their suffixed names.

This plan will be written only after the detailed design is approved. It will
use the repository's normal fresh-context, serial-phase, automated-test,
evidence, Mac commit/push, and Ubuntu fast-forward validation rules.

It will require each phase to prove that its lane changes do not affect the
other lanes before the next phase starts. It will include host-capacity gates,
Mac and both-Linux installation evidence, exact-SHA promotion evidence,
per-lane live acceptance, and migration/rollback proof.

No implementation phases are defined yet.
