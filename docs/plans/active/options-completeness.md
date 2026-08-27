# Options Completeness: Agents, Skills, Sandbox, Checkpointing, QueryOnce (phase 4a)

Closes 5 gaps found during a post-phase-3 parity audit against the Python
reference's `__all__` surface: `AgentDefinition` (subagent config, sent via
`initialize`), `skills` auto-configuration, `SandboxSettings`, file
checkpointing enablement (making phase 1's `RewindFiles` control method
actually usable), and a one-shot `QueryOnce` convenience function
(Python's module-level `query()`).

## Acceptance Criteria

### A. Agents (`AgentDefinition`)

1. `type AgentDefinition struct { Description, Prompt string; Tools, DisallowedTools []string; Model string; Skills []string; Memory string; McpServers []string; InitialPrompt string; MaxTurns int; Background bool; Effort string; PermissionMode string }` — field names map directly to Python's already-camelCase wire keys (`disallowedTools`, `mcpServers`, `initialPrompt`, `maxTurns`, `permissionMode`); zero-value fields are omitted on the wire (`omitempty` on every field, since Python drops `None` values from the dict it sends).
2. `WithAgents(agents map[string]AgentDefinition) Option` stores them; at `New()` time, alongside the existing `hooks` payload, `initialize`'s request gains an `"agents"` key: `{"<name>": {...camelCase fields...}}` (omit the key entirely if no agents registered — same "don't send empty" policy already used for `hooks`).

### B. Skills

3. `WithSkills(skills ...string) Option` and a sentinel `WithAllSkills() Option` (Python's `Literal["all"]`) — mutually exclusive with each other (last-set-wins is fine, no need to error).
4. When skills are set (either form): `initialize`'s request gains a `"skills"` key with the list — **only** for the explicit-list form, never for `WithAllSkills()` (matches Python: `"all"`/omitted sends nothing).
5. Skills auto-configure two other things, matching Python's `_apply_skills_defaults`: (a) `--allowedTools` gains synthesized entries — `"Skill"` if `WithAllSkills()`, or `"Skill(<name>)"` per named skill — unioned with whatever `WithAllowedTools` already set; (b) if `WithSettingSources` was never called, skills being set defaults `--setting-sources` to `user,project` (do not override an explicit `WithSettingSources` call).

### C. Sandbox

6. `type SandboxSettings struct { Enabled *bool; AutoAllowBashIfSandboxed *bool; ExcludedCommands []string; AllowUnsandboxedCommands *bool; Network *SandboxNetworkConfig; IgnoreViolations *SandboxIgnoreViolations; EnableWeakerNestedSandbox *bool }`, `SandboxNetworkConfig{AllowedDomains, DeniedDomains []string; AllowManagedDomainsOnly, AllowUnixSockets, AllowAllUnixSockets, AllowLocalBinding, AllowMachLookup *bool; HTTPProxyPort, SOCKSProxyPort *int}`, `SandboxIgnoreViolations{File, Network []string}` — all pointer/omitempty so an unset field never appears on the wire.
7. `WithSandbox(s SandboxSettings) Option` — at flag-build time, JSON-merges `sandbox` into whatever `--settings` value `WithSettings` produced: if `WithSettings` was also called, parse its value as a JSON object, add/overwrite a `"sandbox"` key with the marshaled `SandboxSettings`, re-serialize as the final `--settings` value; if `WithSettings` wasn't called, the `--settings` flag is just `{"sandbox": {...}}`. If `WithSettings`'s value isn't valid JSON (e.g. a file path), return a `New()` error when `WithSandbox` is also set (same "can't merge into an opaque value" pattern already established for `WithMCPConfig`+`WithSDKMcpServer` in phase 3).

### D. File checkpointing

8. `WithEnableFileCheckpointing() Option` — sets env var `CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=true` on the subprocess (merged via the existing `buildEnv` path from phase 1, same precedence rules: caller-supplied `WithEnv` values still win if they set this key explicitly). Doc comment must state plainly: this alone does not populate `UserMessage.UUID` values to pass to `RewindFiles` — callers also need `WithExtraArgs(map[string]*string{"replay-user-messages": nil})`, which already exists from phase 1; don't auto-add it here, just document the requirement (matches the Python reference's own split between these two independent settings).

### E. `QueryOnce`

9. `func QueryOnce(ctx context.Context, worktreePath, text string, updates chan<- Message, opts ...Option) (ResultMessage, error)` — spawns a fresh `Client` via `New`, calls `Prompt`, and `Close`s it before returning (via `defer`), regardless of whether `Prompt` succeeded or errored. This is pure sugar over `New`+`Prompt`+`Close` — no new subprocess-lifecycle logic, just the convenience wrapper Python's module-level `query()` provides. If `New` itself fails, return its error directly (no `Close` needed, nothing to close).

## Test Scenarios

- `WithAgents` produces the exact `"agents"` key/shape in the captured `initialize` request; omitted entirely when unset.
- `WithSkills("a","b")` → `initialize` carries `"skills":["a","b"]`; `WithAllSkills()` → no `"skills"` key at all, but still triggers the `Skill` allowedTools/setting-sources defaulting.
- Skill-derived `allowedTools` entries union correctly with explicit `WithAllowedTools` entries (no duplication, order doesn't need to be exact — just presence).
- `WithSettingSources` explicitly set is NOT overridden by skills being present.
- `WithSandbox` alone produces `--settings {"sandbox":{...}}`; combined with `WithSettings('{"foo":1}')` merges to `{"foo":1,"sandbox":{...}}`; combined with a non-JSON `WithSettings` value returns a `New()` error.
- Every pointer field left nil in `SandboxSettings`/`SandboxNetworkConfig`/`SandboxIgnoreViolations` is absent from the marshaled JSON (no `null` clutter).
- `WithEnableFileCheckpointing()` sets the env var on the spawned subprocess (assert via the fake-CLI harness's env-capture scenario, same pattern phase 1's `TestClient_EnvMerge` used); a caller-supplied `WithEnv` value for the same key still wins.
- `QueryOnce` against the fake CLI's two-turn scenario returns the correct `ResultMessage` and the subprocess is confirmed closed afterward (no leaked process); an error from `New` (e.g. bad `WithCLIPath`) propagates directly.

## Decisions

- All five items bundled into one plan/one implementation pass because they're all small, independent additions to the same file (`client.go`'s options/`buildArgs`/`New()`) — splitting them into separate worktrees would only create merge risk for no parallelism benefit (single shared file).
- `AgentDefinition`/`SandboxSettings` intentionally use pointer/`*bool` fields for "unset" tri-state clarity, matching how phase 1 handled `WithMaxBudgetUSD`'s explicit-zero-vs-unset problem — consistent idiom for optional-with-meaningful-zero-value fields across this codebase.

## Progress

Not started.

## Validation

Not yet applicable.
