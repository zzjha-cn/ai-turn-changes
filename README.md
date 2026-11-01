# cxr — Codex Rewind

`cxr` adds turn-level rewind to the Codex CLI. Before each turn it records a
workspace snapshot; after a turn finishes it records the resulting snapshot.
Rewinding forks the conversation at a chosen turn with the Codex app-server and
restores the files to the matching snapshot. The original thread is never
modified or truncated.

Rewind is a **soft fork**:

- the original Codex thread and its history stay intact;
- a new thread is created with history cut at the selected turn;
- files are restored in the Git working tree;
- `cxr` prints `codex resume <new-thread-id>` so you can continue there.

All state is stored in a self-contained `<repo>/.codex-rewind/` directory. There
is no daemon, no SQLite, no PTY and no modification to Codex itself.

## Status

Version 0.1.0. Implemented and tested on macOS with Codex CLI 0.164.0. Rewind
uses the app-server `thread/fork` API; `doctor` reports whether the installed
Codex exposes the required turn selectors.

## Requirements

- Go 1.22 or newer to build (the runtime is a single static binary).
- Codex CLI with app-server support. The testing baseline is 0.164.0.
- A Git working tree. Tracked and untracked, non-ignored files are captured;
  ignored files are not.

## Build and install

```bash
go build -o bin/cxr ./cmd/cxr
install -m 0755 bin/cxr ~/.local/bin/cxr   # or anywhere on PATH
```

Install the lifecycle hooks. Codex hooks are not trusted automatically:

```bash
cxr install --user    # writes <CODEX_HOME>/hooks.json, applies to all projects
cxr install --repo    # writes <repo>/.codex/hooks.json, applies to one project
```

Review the new hook entries in Codex and approve/trust them. Without trust Codex
will not run them, and `cxr doctor` will warn that hooks are missing or
untrusted. To remove them again:

```bash
cxr uninstall --user
cxr uninstall --repo
```

`--user` uses `$CODEX_HOME/hooks.json` when `CODEX_HOME` is set, otherwise
`~/.codex/hooks.json`. Installation preserves unrelated `hooks.json` content.

The hook command is written as an absolute path to the running `cxr` binary, so
keep that binary where it is (or reinstall after moving it).

## Quick start

```bash
cd /path/to/repo
cxr install --user
# ... run Codex normally; hooks record up to 10 completed turns ...
cxr list
cxr show --session <session-id> --seq 3
cxr rewind --session <session-id> --seq 3 --before   # undo turn 3 and later
codex resume <new-thread-id>
```

`--before` restores the files as they were before turn 3 and forks history so
the new thread ends before turn 3. `--through` keeps turn 3's result and forks
after it. `--last` is shorthand for `--before` on the newest retained turn.

## Commands

```
cxr hook [--root DIR] [--verbose]
cxr list [--session ID] [--all] [--json]
cxr show --session ID --seq N [--json]
cxr rewind [--last | --session ID --seq N | --session ID --turn T]
           [--before | --through] [--dry-run] [--resume] [--force]
cxr clean [--older-than 30d] [--dry-run] [--yes]
          [--include-stale-open] [--include-incomplete] [--orphans] [--json]
cxr gc [--dry-run] [--yes] [--json]
cxr doctor [--json] [--codex PATH] [--timeout 20s]
cxr status [--json]
cxr install (--user | --repo)
cxr uninstall (--user | --repo)
cxr version
```

Every command accepts `--root DIR` to select a repository root explicitly.
Without it, `cxr` resolves the Git top-level directory from the current working
directory; hooks run with the session's working directory.

`cxr hook` is the hidden hook entry point. It reads one JSON object from stdin
and always exits zero unless `strictHook` is enabled in the config; hook
failures are logged and never block Codex.

### Selecting a turn

- `--seq` and `--turn` refer to one session. `--seq` is the retained sequence
  number shown by `cxr list`.
- Without `--session`, the most recently active session is used.
- Only the newest 10 terminal turns are retained per session. Active turns do
  not count against the limit. A turn that has fallen out of the window cannot
  be selected; `cxr` refuses rather than silently picking a newer turn.
- `--turn` requires the full turn UUID.

### Rewind modes and options

| Mode | Conversation | Files |
| --- | --- | --- |
| `--before` | fork with `beforeTurnId = T` | restore `T`'s baseline state |
| `--through` | fork with `lastTurnId = T` | restore `T`'s final state |
| `--last` | same as `--before` on the newest retained turn | same |

- `--dry-run` prints the plan and conflicts without forking or changing files.
- `--resume` continues an unfinished rewind operation. If the operation already
  created a thread, that thread is reused and never forked twice.
- `--force` overwrites files that were changed outside the discarded turns and
  clears stale active markers left by a crash. It does **not** bypass a turn that
  is still running or a held lock.
- `--through` on an interrupted turn without a final snapshot is rejected.

Successful output includes the new thread id and the exact resume command:

```text
Rewind completed: turn 3 --before
New thread: 01a19...
Continue with:
  codex resume 01a19...
```

### Conflicts

Before restoring, `cxr` compares each path touched by the discarded turns with
the latest recorded snapshot. A mismatch means the file was edited outside those
turns (for example, manually in an editor). The default behavior is to stop
without touching anything and list the conflicting paths. Re-run with
`--force` only after understanding that those manual changes will be lost.

A file deleted by a discarded turn is restored. A file introduced by a
discarded turn is deleted, but only if it still matches the recorded content.
Files never touched by the discarded turns, including unrelated untracked
files, are left alone. A missing or corrupt snapshot object makes the rewind
fail closed before any file is changed.

### Status, doctor and JSON

`cxr status` summarizes the local store: sessions, retained turns, active turns,
incomplete operations, state/object counts and disk usage.

`cxr doctor` checks the data directory, queue/item consistency, missing
snapshot objects, locks, unfinished rewinds, hook installation/trust, the Codex
binary and app-server capabilities. It exits 1 when it finds an error.

`list`, `show`, `status`, `doctor`, `clean` and `gc` accept `--json`. CLI JSON
is intended for scripts and is not schema-versioned; the JSON files persisted
under `.codex-rewind/` are versioned.

## Retention and cleanup

Only the 10 newest terminal turns per session are retained. When an 11th turn
finishes, the oldest turn item is dropped from the queue. Its baseline snapshot
often remains referenced by the next retained turn, so pruning is
reference-aware. If `queue.json` is corrupted, `cxr` rebuilds it from the turn
items under the repository lock.

`cxr clean` expires whole sessions by their last activity time. The default age
is 30 days (configurable) and the default action is a dry run:

```bash
cxr clean --older-than 30d             # show the plan
cxr clean --older-than 30d --dry-run   # same, explicit
cxr clean --older-than 30d --yes       # delete
```

Options:

- `--include-stale-open` also expires sessions that never closed cleanly.
- `--include-incomplete` also deletes old **incomplete rewind operations**.
  Without it they are never silently removed.
- `--orphans` is accepted for compatibility; orphan state/object collection is
  always part of the plan.

Shared objects are reference-counted through reachability, not deleted just
because one session expires. `cxr gc` performs the same reachability collection
on its own and also defaults to a dry run; pass `--yes` to delete.

Objects referenced by retained baselines/finals and by the safety/target states
of incomplete rewind operations are kept.

## Data layout

```text
<repo>/.codex-rewind/
  .gitignore              contains "*", so the directory never shows in git status
  config.json             schemaVersion 1 settings
  lock                    repository-wide flock (metadata records the holder)
  objects/<aa>/<62 hex>   content-addressed SHA-256 blobs
  states/<state-id>.json  manifest of paths, modes, sizes and object ids
  turn/<session-id>/
    session.json          thread id, title, timestamps, open/closed
    queue.json            ordered 10-turn window and next sequence number
    items/<seq>-<turn>.json  prompt, assistant text, changes, snapshot ids
    events/               compact PostToolUse events
  rewind/<op-id>.json     rewind journal and phase
  logs/                   hook failure logs
  tmp/                    atomic-write scratch space
```

All persisted JSON carries `schemaVersion`. Writes use a temporary file in the
same directory, `fsync`, then atomic `rename`. The data directory and its
subdirectories are created with mode `0700`; metadata files are `0600` and CAS
objects are read-only `0444`.

`config.json` is created with defaults. Example:

```json
{
  "schemaVersion": 1,
  "retention": {"maxRetainedTurns": 10, "cleanAfterDays": 30},
  "limits": {
    "maxPromptBytes": 65536,
    "maxAssistantBytes": 65536,
    "maxEventBytesPerTurn": 1048576,
    "maxEventsPerTurn": 200,
    "maxDisplayFiles": 10,
    "maxDisplayTextChars": 500
  },
  "rewind": {"defaultMode": "fork", "allowInPlace": false},
  "hooks": {"recordPostToolUse": true}
}
```

Setting `strictHook` to `true` makes hook failures return a non-zero exit code;
the default is fail-open so rewind bookkeeping never breaks a Codex session.

## Rewind transaction and crash recovery

A rewind is journaled as:

```text
planned -> safety_captured -> thread_forked -> files_restoring
        -> verified -> completed
```

Failures are recorded as `failed_retryable` or `failed_terminal`. If the fork
step fails, no files are changed and the operation can be retried. If restoring
fails, `cxr` restores the safety snapshot and keeps the new thread id for
diagnosis; a retry never forks again. `cxr doctor` shows the phase, thread id,
safety state and whether the operation can be resumed.

## Environment variables

- `CODEX_HOME` — Codex home used by `install --user`, `doctor` and app-server.
- `CXR_CODEX_BIN` — Codex binary used by rewind/doctor (overrides `--codex` and
  the `codex` on `PATH` for rewind; `doctor` also accepts `--codex`).

## Limitations

`cxr` is file-level undo for a Git working tree, not a system snapshot. It does
not restore:

- the Git index or staging area;
- `.git/` contents;
- Git-ignored files;
- databases or external services;
- network requests, containers or background processes;
- files created outside the repository.

Rewind can therefore undo source edits while leaving a database migration, a
package install, a running server or an API side effect in place. Snapshot
storage can also contain sensitive uncommitted content: the data directory is
private (`0700`), but it is local plaintext. Clean old sessions when they are no
longer needed.

The v1 CLI intentionally has no in-place revert (`--in-place` is rejected).
`thread/revert` remains a future option.

## Development

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
```

The tests use temporary Git repositories and fake app-server processes. To probe
a real installed Codex handshake (skipped by default):

```bash
CXR_TEST_REAL_CODEX=1 go test ./internal/codex/appserver -run TestRealCodexHandshakeOptional -v
```

Design notes live in:

- `docs/codex-rewind-design-and-implementation.md`
- `docs/turn-based-workspace-state-undo-guide.md`