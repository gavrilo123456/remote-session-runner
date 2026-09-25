# Repository workflow for Remote Session Runner

## Authoritative source checkout

Make all Runner source-code, test, schema, deployment-file, and project-documentation changes in the Mac checkout at `/Users/tomasz.walczuk/projects/remote-session-runner`. This checkout is the development source for the current PoC. Do **not** edit tracked project files directly in the Ubuntu checkout at `/home/ubuntu/projects/remote-session-runner`, and do not copy changed source files there with `scp` or `rsync`.

Host-specific secrets, service state, and runtime configuration belong outside the Git checkout. Never commit private keys or credentials. Host installation and tests may use the Ubuntu machine, but changes to versioned files must return through the Mac checkout and Git.

## Commit, push, then fast-forward the test host

For every completed change or implementation phase, use this order:

1. On the **Mac**, follow the applicable design and phased-plan gates, inspect the diff, and commit only the intended files on `dev`. Preserve unrelated or in-progress files in the worktree.
2. On the **Mac**, push that commit to `origin/dev` using the dedicated GitHub key at `/Users/tomasz.walczuk/.ssh/gavrilo123456-github`. Verify the push succeeded; do not start remote validation against an unpushed commit.
3. On **Ubuntu**, first verify `/home/ubuntu/projects/remote-session-runner` is on `dev` with a clean worktree. Pull `origin/dev` with `--ff-only` using `/home/ubuntu/.ssh/gavrilo123456-github`. Verify the resulting Ubuntu `HEAD` equals the commit pushed from the Mac before building or testing that revision.
4. Record the Mac commit, GitHub branch, Ubuntu commit, commands, and test results in the phase evidence. Do not call a phase delivered on Ubuntu or start the next phase until the required push, pull, and validation are complete.

The current remote is `git@github.com:gavrilo123456/remote-session-runner.git`. Use explicit SSH identities; neither checkout should depend on an agent or a default SSH key:

```sh
# Mac, in /Users/tomasz.walczuk/projects/remote-session-runner
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Ubuntu, in /home/ubuntu/projects/remote-session-runner, after checking branch and status
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

If either checkout is dirty, the branches diverge, authentication fails, or a pull is not fast-forwardable, stop and report the exact state. Do not force-push, reset, overwrite local files, or skip the GitHub handoff. The serial phase/test requirements in the detailed phased implementation plan still apply.
