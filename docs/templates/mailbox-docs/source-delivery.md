# Deliver Mac source to Linux for a small test

This is a source-copy method through the existing mailbox, not a remote Git
workflow. Follow the project's `AGENTS.md` and [main handbook](README.md).
It permits testing reviewed, **nonsecret** source bytes without creating a
persistent Linux checkout or changing a remote Git repository.

## Where the files go

A normal `run` without `source` uses Runner's fresh, private session workspace.
The script below creates a new `source/` child there and runs the test from it.
Its actual absolute path is printed in the result; do not guess a path such as
`/home/ubuntu/projects/{{PROJECT_NAME}}`. The mailbox's top-level
`repository_alias` does not synchronize a checkout.

The snapshot exists only for that session. **Copy and test in the same request.**
Confirmed closed teardown removes the owned session workspace. A later request
gets a new workspace; it cannot reuse this path. Never manually clean up a
LOST/unconfirmed session to obtain a fresh test.

## 1. Review the exact Mac inputs

1. Edit source only in `{{PROJECT_ROOT}}` on the Mac.
2. Select every file required for this test, including relevant build manifests
   and fixtures. Exclude secrets, credentials, private keys, mailbox/runtime
   files, caches and `.git`; do not blindly copy a checkout or untracked files.
3. Read the exact selected bytes and record their SHA-256 hashes. Use only the
   direct read operations allowed by `AGENTS.md`; for a reviewed source file:

   ```sh
   # Mac — single read-only command; replace the example path
   shasum -a 256 -- path/to/reviewed-file.py
   ```

4. Encode those same bytes and expected hashes into the bounded request script
   using native file editing. Preserve bytes exactly; JSON escaping is not
   literal source content. Never normalize line endings, add or drop a final
   newline, or change encoding while copying; the staged bytes must match the
   Mac hash. Do not execute a Mac helper to publish the request.
5. Record the file list/hashes and the Mac commit/diff context. If source changes
   during the test, qualify which snapshot was tested; do not claim the new
   working tree passed.

## 2. Complete example: one nonsecret source file

This illustrative `probe.py` contains exactly these 28 ASCII bytes, including
the final newline:

```python
print('SOURCE_SNAPSHOT_OK')
```

Its SHA-256 is
`21e7fab2be24fed8715b193fca679609db21ba47eab41e76de6604f4fec23649`.

**Replace the example content and hash with actual reviewed Mac source** for
real project tests. Also replace `UNIQUE` with a fresh safe value. The example
does not claim that `probe.py` already exists in the project.

Create `{{MAILBOX_ROOT}}/inbox/<request_id>.json` with this complete JSON:

```json
{
  "request_id": "req-{{PROJECT_NAME}}-source-snapshot-UNIQUE",
  "idempotency_key": "{{PROJECT_NAME}}-source-snapshot-UNIQUE",
  "operation": "run",
  "repository_alias": "{{PROJECT_NAME}}",
  "script": "set -euo pipefail\npython3 - <<'PY_SNAPSHOT'\nimport hashlib\nimport os\nfrom pathlib import Path, PurePosixPath\nfiles = [\n    ('probe.py', b\"print('SOURCE_SNAPSHOT_OK')\\n\", '21e7fab2be24fed8715b193fca679609db21ba47eab41e76de6604f4fec23649')\n]\nroot = Path.cwd() / 'source'\nroot.mkdir(mode=0o700)\nprint('snapshot_root=' + str(root.resolve()))\nseen = set()\nfor name, content, expected in files:\n    if name in seen:\n        raise ValueError('duplicate source path')\n    seen.add(name)\n    relative = PurePosixPath(name)\n    if not name or not relative.parts or relative.is_absolute() or relative.as_posix() != name or any(part == '..' or part.casefold() == '.git' for part in relative.parts):\n        raise ValueError('unsafe source path')\n    if hashlib.sha256(content).hexdigest() != expected:\n        raise ValueError('source bytes do not match reviewed Mac hash')\n    parent = root\n    for part in relative.parts[:-1]:\n        parent = parent / part\n        try:\n            parent.mkdir(mode=0o700)\n        except FileExistsError:\n            if parent.is_symlink() or not parent.is_dir():\n                raise ValueError('unsafe source directory')\n    destination = parent / relative.name\n    fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)\n    with os.fdopen(fd, 'wb') as output:\n        output.write(content)\n    actual = hashlib.sha256(destination.read_bytes()).hexdigest()\n    if actual != expected:\n        raise ValueError('staged source hash mismatch')\n    print('snapshot_sha256 ' + name + ' ' + actual)\nPY_SNAPSHOT\ncd source\npython3 probe.py\n",
  "timeout_seconds": 30
}
```

Read/validate the JSON, then create its matching empty owner-owned regular
`<request_id>.ready` marker **last**, as in the main handbook. Use exact `0644`
for direct workspace publication, or retain exact `0600` when using the native
creation mode described there; no other input mode is accepted. Both selection
fields and `source` are omitted; execution uses the configured Linux default
and an empty session workspace. The example requires `python3` on Linux.

The guarded manifest supports relative subdirectories. It refuses a pre-existing
`source/` directory, absolute/noncanonical paths, `..`, `.git`, duplicate
files and existing symlink destinations. Keep these checks when adapting it.
Do not replace them with writes into a shared checkout or an unverified path.
This is scoped file creation under the session workspace, not adversarial
isolation from other processes running as `ubuntu`.

## 3. Verify and preserve the result

Use the same request's terminal response and advertised events. For this
example, expect its printed absolute snapshot path, the exact per-file hash,
`SOURCE_SNAPSHOT_OK`, exit `0`, complete untruncated output and closed teardown.
A matching hash proves the copied bytes; the passing command proves only this
example. For project tests, require their own intended assertions and outcome.

Preserve the manifest, terminal response and event prefix before publishing
the exact terminal ACK. An upload that succeeded but whose tests failed is a
failed test; never claim success from the hash output alone. If execution or
teardown is uncertain, preserve the existing identity and ask the maintainer.

## Size and setup limits

The **decoded script** must fit **128 KiB / 131,072 UTF-8 bytes** and the full
serialized JSON request must fit **1 MiB / 1,048,576 bytes**. Escaping or binary
encoding adds overhead. Use this method only for small reviewed snapshots;
do not split a large repository across unrelated sessions to evade the limits.

This setup does not provide automatic Git source delivery. Larger snapshots
need a separately supported and authorized source-transfer/checkout mechanism
with a known Linux working directory, exact source identity and credentials.
Do not enable `git_revision`, invent a repository alias, fetch/pull/clone on
Linux, or change Runner policies to work around that missing setup.

A snapshot test is not proof that the source was committed, pushed, imported,
released or deployed. It does not add execution or network permission beyond
the task and project policy.
