# Repository workflow for Remote Session Runner

## Authoritative source checkout

Make all Runner source-code, test, schema, deployment-file, and project-documentation changes in the Mac checkout at `/Users/tomasz.walczuk/projects/remote-session-runner`. This checkout is the development source for the current PoC. Do **not** edit tracked project files directly in the Ubuntu checkout at `/home/ubuntu/projects/remote-session-runner`, and do not copy changed source files there with `scp` or `rsync`.

Host-specific secrets, service state, and runtime configuration belong outside the Git checkout. Never commit private keys or credentials. Host installation and tests may use the Ubuntu machine, but changes to versioned files must return through the Mac checkout and Git.

## Commit to primary, verify the mirror, then fast-forward the test hosts

For every completed change or implementation phase, use this order:

1. On the **Mac**, follow the applicable design and phased-plan gates, inspect the diff, and commit only the intended files on `dev`. Preserve unrelated or in-progress files in the worktree.
2. On the **Mac**, push that commit to the authoritative local Gitea `origin/dev` using `/Users/tomasz.walczuk/.ssh/local-gitea-key`. Verify the primary push succeeded; do not start remote validation against an unpushed primary commit.
3. Gitea automatically mirrors to GitHub. On the **Mac**, verify `github/dev` resolves to the exact primary commit. The mirror is a distribution path for the Ubuntu hosts; do not manually push it or treat it as source authority. If it has not caught up, stop and report the primary commit and observed mirror commit.
4. On each **Ubuntu** host, first verify `/home/ubuntu/projects/remote-session-runner` is on `dev` with a clean worktree. Its `origin` is the reachable GitHub mirror, so pull `origin/dev` with `--ff-only` using `/home/ubuntu/.ssh/gavrilo123456-github`. Verify the resulting Ubuntu `HEAD` equals the verified primary and mirror commit before building or testing that revision.
5. Record the Mac commit, primary Gitea branch, verified GitHub mirror commit, both Ubuntu commits, commands, and test results in the phase evidence. Do not call a phase delivered on Ubuntu or start the next phase until the required primary push, mirror verification, pulls, and validation are complete.

The Mac primary remote is `ssh://local-gitea/myorganization/remote-session-runner.git`; it resolves locally to Gitea on `localhost:2222`. The Mac `github` remote is `git@github.com:gavrilo123456/remote-session-runner.git` and is a secondary automatic mirror. The two Ubuntu `origin` remotes point to that GitHub mirror because they cannot reach the Mac-local Gitea endpoint. Use explicit SSH identities; neither checkout should depend on an agent or a default SSH key:

```sh
# Mac, in /Users/tomasz.walczuk/projects/remote-session-runner
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Mac: wait for the automatic GitHub mirror to contain the same commit.
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Ubuntu, in /home/ubuntu/projects/remote-session-runner, after checking branch and status
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

If either checkout is dirty, the primary/mirror commits differ, authentication fails, or a pull is not fast-forwardable, stop and report the exact state. Do not force-push, reset, overwrite local files, bypass the primary Gitea handoff, or skip mirror verification. The serial phase/test requirements in the detailed phased implementation plan still apply.

## Go cache and temporary files

On the Mac, use the shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and `GOMODCACHE=/private/tmp/remote-session-runner-gomodcache` configured by the Makefile. Reuse these across phases; do not create a new cache or module-cache copy per phase. On Ubuntu, reuse the selected account's normal Go caches. Use `t.TempDir()` for test fixtures and shell `trap` cleanup for temporary host scripts. Keep generated build/test output outside the Git checkout, and remove only task-owned temporary artifacts after their processes have exited. Do not clean shared or other-project caches.
