# Session Lifecycle: Local-Disk Resume (SDK parity, phase 2)

Phase 2 of the SDK parity port (see `docs/plans/completed/streaming-client-control-protocol.md`
for phase 1). Scope, per the 2026-08-27 scope decision recorded there:
**local-disk resume only** — CLI flags to resume/continue/fork an existing
local session, plus read-only Go equivalents of the Python reference's
local session-listing/reading functions. The pluggable `SessionStore`
abstraction, resume materialization into a temp `CLAUDE_CONFIG_DIR`,
`session_mutations` (rename/tag/delete/fork), `session_summary` folding,
`session_import`, and the transcript-mirror batcher are explicitly out of
scope — none of that machinery exists to serve this Go SDK's actual use
case (driving local `claude` CLI subprocesses), only to support external
non-file-backed session storage.

Research basis: `refs/claude-agent-sdk-python/src/claude_agent_sdk/_internal/sessions.py`
(local on-disk format, read-only) and
`refs/claude-agent-sdk-python/src/claude_agent_sdk/_internal/transport/subprocess_cli.py`
(resume-related CLI flags), both already read in full during phase 1's
research pass — no new research agents were needed for this plan.

## Acceptance Criteria

### A. Resume-related CLI flags

1. Six new functional options add the resume-related flags the phase-1
   flag catalog explicitly excluded, matching these exact conditions
   (verified against the Python reference in phase 1's research):
   - `WithContinueConversation()` → `--continue` (bool flag, present only
     when called)
   - `WithResume(sessionID string)` → `--resume=<sessionID>` (`=`-joined
     form — an injection guard in the Python reference, carried forward
     here even though Go's flag construction isn't vulnerable to shell
     injection the way Python's subprocess argument handling can be; kept
     for wire/behavioral parity with what the CLI expects)
   - `WithSessionID(sessionID string)` → `--session-id=<sessionID>`
   - `WithForkSession()` → `--fork-session` (bool flag)
   - `WithResumeSessionAt(entryUUID string)` → `--resume-session-at=<entryUUID>`
   - `WithResumeDropsTurn(turnUUID string)` → `--resume-drops-turn=<turnUUID>`
     (Python's condition is "is not None", not truthiness, meaning even an
     explicitly-empty value is forwarded — in Go, calling the option at all
     means "explicit", so `WithResumeDropsTurn("")` must still emit
     `--resume-drops-turn=`, not omit the flag)
2. No client-side validation of `sessionID`/`entryUUID`/`turnUUID` values
   (e.g. no UUID-format check) — the Python reference's own CLI-flag
   construction layer doesn't validate these either (UUID validation lives
   in the store-backed resume-materialization path, which is out of scope
   here); malformed values are the CLI's problem to reject.
3. These flags compose with existing options unchanged — e.g.
   `WithResume` + `WithPermissionMode` + `WithModel` together still produce
   every flag each option implies, in the same relative order the existing
   flag-building code already establishes for other options.

### B. Read-only local session listing/reading

4. A new set of package-level functions (not `Client` methods — these read
   local files directly, independent of any running CLI subprocess,
   matching the Python reference's free-function design):
   ```go
   func ListSessions(opts ListSessionsOptions) ([]SDKSessionInfo, error)
   func GetSessionInfo(sessionID string, directory string) (*SDKSessionInfo, error)
   func GetSessionMessages(sessionID string, directory string, limit, offset int) ([]SessionMessage, error)
   func ListSubagents(sessionID string, directory string) ([]string, error)
   func GetSubagentMessages(sessionID, agentID string, directory string, limit, offset int) ([]SessionMessage, error)
   ```
   `ListSessionsOptions{Directory string; Limit int; Offset int; IncludeWorktrees bool}`.
   `directory`/`Directory` empty string means "scan every project directory
   under the config home" (matching Python's `directory=None` default for
   `list_sessions`) — this is NOT "default to cwd"; a caller wanting
   cwd-scoped results must pass `os.Getwd()`'s result explicitly.
5. Path resolution replicates the Python reference's algorithm exactly:
   config home is `$CLAUDE_CONFIG_DIR` (env var) or `~/.claude`; project
   directory is `<config_home>/projects/<sanitized(realpath(project_dir))>`
   where sanitization replaces every non-`[a-zA-Z0-9]` character with `-`,
   and — only when the sanitized name exceeds 200 characters — truncates to
   200 chars plus a hash suffix computed with the same 32-bit JS-style hash
   Python replicates (`h = (h<<5) - h + charCode`, wrapped to signed
   32-bit, absolute value, base36-encoded), with prefix-scan fallback if
   the exact sanitized directory isn't found (compensating for a
   Bun-vs-Node hash mismatch in the CLI itself — same reasoning applies to
   a Go port, since Go computes this hash independently of whatever
   runtime produced the directory).
6. Session file layout: `<project_dir>/<session_id>.jsonl` (NDJSON, one
   transcript entry per line), UUID-validated session ID. Subagent
   transcripts: `<project_dir>/<session_id>/subagents/**/agent-<agent_id>.jsonl`
   (recursive — subagents can nest under `subagents/workflows/<runId>/`
   etc.), with a metadata sidecar `agent-<agent_id>.meta.json` (single JSON
   object, not NDJSON) carrying `toolUseId`/`parentAgentId`.
7. `GetSessionMessages`/`GetSubagentMessages` reconstruct the conversation
   chain from the flat NDJSON entry list via the `uuid`/`parentUuid` graph:
   index all entries by `uuid`, find terminal entries (no other entry's
   `parentUuid` points to them), prefer non-sidechain/non-team/non-meta
   "main chain" terminals (latest by file position among ties), walk
   backward via `parentUuid` to the root, reverse for chronological order.
   `logicalParentUuid` (present on `compact_boundary` entries) is
   deliberately NOT followed, so post-compaction history isn't
   double-counted — only the surviving `isCompactSummary` entry represents
   pre-compaction content. Visible messages exclude `isMeta`/`isSidechain`/
   `teamName`-bearing entries but include `isCompactSummary` ones.
8. `ListSessions`/`GetSessionInfo` use the cheaper "lite" path: read only
   the first and last 64KB of each session file (not a full parse) and
   extract `customTitle`/`aiTitle`/`lastPrompt`/`summary`/`gitBranch`/`cwd`/
   `tag`/`timestamp` via substring scanning (not full JSON parsing, since
   the head/tail buffers can be truncated mid-object) — this is a
   deliberate performance optimization from the Python reference worth
   preserving, not an implementation detail to simplify away, since full-
   parsing every session file just to list them would be far slower for a
   caller with many sessions.
9. `ListSessions` with `IncludeWorktrees: true` (default in the Python
   reference) also includes sessions from the directory's git worktrees
   (via `git worktree list --porcelain`, 5s timeout, empty result on any
   failure — never an error).
10. `SDKSessionInfo` and `SessionMessage` field sets match the Python
    reference exactly (see Decisions for the exact Go struct shapes).

## Test Scenarios

**CLI flags**
- Each of the 6 new options, alone and combined with existing phase-1
  options, produces the exact documented flag(s) in argv (exact-argv
  assertion against the fake-CLI harness, matching phase 1's test style).
- `WithResumeDropsTurn("")` still emits `--resume-drops-turn=` (not
  omitted) — the one case where an option's zero-value argument must still
  produce a flag.
- Options left unset produce no flag (regression-consistent with phase 1).

**Session listing/reading — use a temp directory as a fake `$CLAUDE_CONFIG_DIR`**
- `ListSessions` against a temp project tree with 3 sessions returns all 3,
  sorted by `last_modified` descending, respecting `Limit`/`Offset`.
- `ListSessions` with `Directory: ""` scans multiple project directories
  (create 2 fake project dirs under the temp config home, confirm sessions
  from both are returned).
- A session whose sanitized project-directory name would exceed 200 chars
  round-trips correctly (construct a long path, verify the truncated+hash
  directory name matches, and that lookup finds it).
- `GetSessionMessages` on a transcript with a `parentUuid` chain including
  a `compact_boundary`/`logicalParentUuid` entry does NOT double-count
  pre-compaction messages — only the chain reachable via `parentUuid`
  (excluding `logicalParentUuid`) plus the surviving `isCompactSummary`
  entry appear.
- `GetSessionMessages` excludes `isMeta`/`isSidechain` entries but includes
  `isCompactSummary` ones.
- `ListSubagents`/`GetSubagentMessages` against a session with nested
  subagent transcripts (`subagents/workflows/<id>/agent-<id>.jsonl`)
  correctly discovers and reads them, including the `.meta.json` sidecar's
  `toolUseId`/`parentAgentId`.
- `GetSessionInfo`/`ListSessions`'s lite-parse path correctly extracts
  `customTitle` when present, falls back to `aiTitle`, then `lastPrompt`,
  then `summary`, matching the Python reference's fallback order.
- A corrupted/truncated session file (invalid trailing JSON) doesn't error
  the whole `ListSessions` call — that one session is skipped or returns
  partial info, consistent with the Python reference's fail-safe read
  behavior (this repo's existing "don't fail the whole operation over one
  bad line" philosophy applies here too).
- `ListSessions`/`GetSessionInfo` on an empty/nonexistent config directory
  returns an empty slice / `nil, nil` respectively, not an error.

## Decisions

- **Scope boundary re-confirmed**: this plan does NOT include
  `session_mutations.py`'s local-disk functions (`rename_session`,
  `tag_session`, `delete_session`, `fork_session`) even though they
  operate purely on local files with no `SessionStore` dependency. The
  original scope decision's wording bundled "session_mutations" into the
  excluded pluggable-store group without carving out its local-disk-only
  variants specifically. **This is worth revisiting** — mutation support
  (especially `fork_session` and `delete_session`) may have real standalone
  value independent of the `SessionStore` question. Flagging this
  explicitly rather than silently including or excluding it: if mutation
  support turns out to be wanted, it's a small additive follow-on to this
  plan, not a redesign.
- **`session_resume.py` is out of scope entirely** — that file's whole
  purpose is materializing a `SessionStore`-backed session into a temp
  local directory so the CLI's normal file-based resume can run against
  it. Since this Go port isn't implementing the pluggable `SessionStore`
  abstraction, `session_resume.py` has no local-disk-only subset to port;
  the CLI flags alone (section A) are sufficient for "resume a session
  that already exists as a local file."
- **Free functions, not `Client` methods**: matches the Python reference's
  design (`list_sessions`, `get_session_info`, etc. are module-level
  functions, not methods on `ClaudeSDKClient`) — these operate on the
  filesystem directly and have no relationship to a running CLI subprocess
  or `Client` instance.
- **Exact struct shapes** (Go names, JSON tags only where the field would
  round-trip over a wire boundary — these are pure in-memory return types,
  not wire-protocol types, so JSON tags are for documentation/consistency
  rather than load-bearing):
  ```go
  type SDKSessionInfo struct {
      SessionID    string
      Summary      string
      LastModified int64  // epoch ms
      FileSize     *int64 // nil for a session with no resolvable file size
      CustomTitle  string
      FirstPrompt  string
      GitBranch    string
      Cwd          string
      Tag          string
      CreatedAt    *int64 // epoch ms, nil if undeterminable
  }
  type SessionMessage struct {
      Type            string // "user" | "assistant"
      UUID            string
      SessionID       string
      Message         json.RawMessage // raw Anthropic API message object, not decoded further
      ParentToolUseID string
      ParentAgentID   string
  }
  ```

## Progress

Not started.

## Validation

Not yet applicable.
