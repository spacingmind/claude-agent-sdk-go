package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Read-only access to the CLI's local session store (the on-disk format
// the `claude` CLI itself reads and writes under $CLAUDE_CONFIG_DIR).
// These are free functions, not Client methods: they operate on local
// files directly and have no relationship to a running CLI subprocess,
// mirroring the Python reference's module-level session functions.

// configHomeDir resolves the CLI's config home: $CLAUDE_CONFIG_DIR when
// set, else ~/.claude. A function variable (not a direct call) so tests
// can point it at a temp directory instead of mutating real env vars or
// $HOME.
var configHomeDir = func() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// SDKSessionInfo describes one session stored on local disk.
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

// SessionMessage is one visible message recovered from a stored session
// transcript.
type SessionMessage struct {
	Type            string // "user" | "assistant"
	UUID            string
	SessionID       string
	Message         json.RawMessage // raw Anthropic API message object, not decoded further
	ParentToolUseID string
	ParentAgentID   string
}

// ListSessionsOptions configures ListSessions.
type ListSessionsOptions struct {
	Directory        string // "" = scan every project dir under the config home
	Limit            int    // 0 = no limit
	Offset           int
	IncludeWorktrees bool
}

const (
	liteReadChunk   = 64 * 1024
	maxSanitizedLen = 200
)

var sessionUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var autoMarkerRe = regexp.MustCompile(`^<[a-zA-Z][a-zA-Z0-9_-]*>`)

// sanitizeProjectDirName replicates the CLI's project-directory-name
// sanitization: realpath the project directory (raw path on failure),
// replace every non-[a-zA-Z0-9] rune with '-', and -- only when the
// result exceeds 200 chars -- truncate to 200 and append '-' plus a
// base36 hash suffix of the full sanitized name (32-bit JS-style hash,
// absolute value, lowercase base36).
func sanitizeProjectDirName(projectDir string) string {
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	var b strings.Builder
	for _, r := range projectDir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) <= maxSanitizedLen {
		return name
	}
	return name[:maxSanitizedLen] + "-" + hashSuffix(name)
}

// hashSuffix computes the CLI's 32-bit string hash over the full
// (untruncated) sanitized name: h = h<<5 - h + byte, wrapping int32
// arithmetic, absolute value, lowercase base36.
func hashSuffix(s string) string {
	var h int32
	for i := 0; i < len(s); i++ {
		h = h<<5 - h + int32(s[i])
	}
	n := int64(h)
	if n < 0 {
		n = -n
	}
	return strconv.FormatInt(n, 36)
}

func projectsRoot() string { return filepath.Join(configHomeDir(), "projects") }

// resolveProjectDir maps a real project directory to its session-storage
// directory under <config_home>/projects. ok=false when no such directory
// exists (including after the truncated-name prefix-scan fallback, which
// compensates for hash mismatches between the runtime that produced the
// directory and this one).
func resolveProjectDir(directory string) (string, bool) {
	sanitized := sanitizeProjectDirName(directory)
	projects := projectsRoot()
	candidate := filepath.Join(projects, sanitized)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate, true
	}
	if len(sanitized) > maxSanitizedLen {
		prefix := sanitized[:maxSanitizedLen] + "-"
		entries, err := os.ReadDir(projects)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
					return filepath.Join(projects, e.Name()), true
				}
			}
		}
	}
	return "", false
}

// sessionFilePath returns <project_dir>/<session_id>.jsonl after
// validating the session ID's UUID shape (an error, never a panic, for a
// non-UUID-shaped ID).
func sessionFilePath(projectDir, sessionID string) (string, error) {
	if !sessionUUIDRe.MatchString(sessionID) {
		return "", fmt.Errorf("claudecode: invalid session ID %q: not a UUID", sessionID)
	}
	return filepath.Join(projectDir, sessionID+".jsonl"), nil
}

// ListSessions lists locally stored sessions, newest first, for one
// project directory (or every project directory when Directory is empty).
func ListSessions(opts ListSessionsOptions) ([]SDKSessionInfo, error) {
	var dirs []string
	if opts.Directory == "" {
		entries, err := os.ReadDir(projectsRoot())
		if err != nil {
			return []SDKSessionInfo{}, nil
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(projectsRoot(), e.Name()))
			}
		}
	} else {
		dir, ok := resolveProjectDir(opts.Directory)
		if !ok {
			return []SDKSessionInfo{}, nil
		}
		dirs = append(dirs, dir)
		if opts.IncludeWorktrees {
			dirs = append(dirs, worktreeProjectDirs(opts.Directory)...)
		}
	}

	var sessions []SDKSessionInfo
	for _, dir := range dirs {
		sessions = append(sessions, listSessionsInProjectDir(dir)...)
	}
	sessions = dedupeSessions(sessions)
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastModified > sessions[j].LastModified
	})
	return applyLimitOffset(sessions, opts.Limit, opts.Offset), nil
}

// listSessionsInProjectDir reads every <uuid>.jsonl directly inside dir
// and lite-parses each, skipping sessions with no derivable summary
// (e.g. sidechain transcripts).
func listSessionsInProjectDir(dir string) []SDKSessionInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SDKSessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sid := strings.TrimSuffix(e.Name(), ".jsonl")
		if !sessionUUIDRe.MatchString(sid) {
			continue
		}
		if info, ok := liteSessionInfo(filepath.Join(dir, e.Name()), sid); ok {
			out = append(out, info)
		}
	}
	return out
}

// GetSessionInfo lite-parses one session's metadata. Returns (nil, nil)
// when the session file doesn't exist.
func GetSessionInfo(sessionID string, directory string) (*SDKSessionInfo, error) {
	dir, ok := resolveProjectDir(directory)
	if !ok {
		return nil, nil
	}
	path, err := sessionFilePath(dir, sessionID)
	if err != nil {
		return nil, err
	}
	if info, ok := liteSessionInfo(path, sessionID); ok {
		return &info, nil
	}
	return nil, nil
}

// GetSessionMessages reconstructs a session's visible conversation from
// its NDJSON transcript: the uuid/parentUuid chain from the latest
// main-chain terminal back to the root (logicalParentUuid deliberately
// not followed), filtered to non-meta/non-sidechain user and assistant
// entries (isCompactSummary kept). limit 0 = no limit; offset/limit apply
// after the full list is built.
func GetSessionMessages(sessionID string, directory string, limit, offset int) ([]SessionMessage, error) {
	dir, ok := resolveProjectDir(directory)
	if !ok {
		return nil, nil
	}
	path, err := sessionFilePath(dir, sessionID)
	if err != nil {
		return nil, err
	}
	msgs, err := readTranscriptFile(path, sessionID)
	if err != nil {
		return nil, err
	}
	return applyLimitOffset(msgs, limit, offset), nil
}

// ListSubagents returns the agent IDs of every subagent transcript stored
// under <project_dir>/<session_id>/subagents/**/agent-<agent_id>.jsonl.
func ListSubagents(sessionID string, directory string) ([]string, error) {
	dir, ok := resolveProjectDir(directory)
	if !ok {
		return []string{}, nil
	}
	if _, err := sessionFilePath(dir, sessionID); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	root := filepath.Join(dir, sessionID, "subagents")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable or missing root: no subagents
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "agent-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if id := strings.TrimSuffix(strings.TrimPrefix(d.Name(), "agent-"), ".jsonl"); id != "" {
			ids[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// GetSubagentMessages reads one subagent's transcript the same way
// GetSessionMessages reads a main session, layering the .meta.json
// sidecar's parentAgentId (all messages) and toolUseId (first message)
// on top.
func GetSubagentMessages(sessionID, agentID string, directory string, limit, offset int) ([]SessionMessage, error) {
	dir, ok := resolveProjectDir(directory)
	if !ok {
		return nil, nil
	}
	if _, err := sessionFilePath(dir, sessionID); err != nil {
		return nil, err
	}
	var path string
	err := filepath.WalkDir(filepath.Join(dir, sessionID, "subagents"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "agent-"+agentID+".jsonl" {
			path = p
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	msgs, err := readTranscriptFile(path, "")
	if err != nil {
		return nil, err
	}
	if meta := readSubagentMeta(dir, sessionID, agentID); meta != nil {
		if meta.ParentAgentID != "" {
			for i := range msgs {
				msgs[i].ParentAgentID = meta.ParentAgentID
			}
		}
		if meta.ParentToolUseID != "" && len(msgs) > 0 && msgs[0].ParentToolUseID == "" {
			msgs[0].ParentToolUseID = meta.ParentToolUseID
		}
	}
	return applyLimitOffset(msgs, limit, offset), nil
}

type subagentMeta struct {
	ParentToolUseID string
	ParentAgentID   string
}

func readSubagentMeta(projectDir, sessionID, agentID string) *subagentMeta {
	var metaPath string
	err := filepath.WalkDir(filepath.Join(projectDir, sessionID, "subagents"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "agent-"+agentID+".meta.json" {
			metaPath = p
		}
		return nil
	})
	if err != nil || metaPath == "" {
		return nil
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var m struct {
		ToolUseID     string `json:"toolUseId"`
		ParentAgentID string `json:"parentAgentId"`
	}
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &subagentMeta{ParentToolUseID: m.ToolUseID, ParentAgentID: m.ParentAgentID}
}

func applyLimitOffset[T any](items []T, limit, offset int) []T {
	if offset > 0 {
		if offset >= len(items) {
			return nil
		}
		items = items[offset:]
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// worktreeProjectDirs resolves the project-storage directories for a
// directory's additional git worktrees. Any failure (git missing, not a
// repo, timeout) yields zero additional worktrees -- never an error.
func worktreeProjectDirs(directory string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", directory, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, line := range strings.Split(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if resolved, err := filepath.Abs(path); err == nil {
			path = resolved
		}
		if path == directory {
			continue
		}
		if dir, ok := resolveProjectDir(path); ok {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// dedupeSessions keeps the newest entry per session ID (worktrees can
// share history and surface the same session under both directories).
func dedupeSessions(sessions []SDKSessionInfo) []SDKSessionInfo {
	seen := make(map[string]int, len(sessions))
	out := make([]SDKSessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if i, ok := seen[s.SessionID]; ok {
			if s.LastModified > out[i].LastModified {
				out[i] = s
			}
			continue
		}
		seen[s.SessionID] = len(out)
		out = append(out, s)
	}
	return out
}

// --- full-parse transcript reading ---

// transcriptEntry is the per-line parse of an NDJSON transcript line;
// only the fields the chain reconstruction needs are typed, everything
// else is ignored.
type transcriptEntry struct {
	Type             string          `json:"type"`
	UUID             string          `json:"uuid"`
	ParentUUID       string          `json:"parentUuid"`
	SessionID        string          `json:"sessionId"`
	Message          json.RawMessage `json:"message"`
	ParentToolUseID  string          `json:"parentToolUseId"`
	TeamName         string          `json:"teamName"`
	IsMeta           bool            `json:"isMeta"`
	IsSidechain      bool            `json:"isSidechain"`
	IsCompactSummary bool            `json:"isCompactSummary"`
}

var transcriptKeepTypes = map[string]bool{
	"user": true, "assistant": true, "progress": true, "system": true, "attachment": true,
}

// readTranscriptFile parses an NDJSON transcript and returns the visible
// conversation messages in chronological order. Corrupt lines are
// silently skipped (this repo's established fail-safe read policy).
func readTranscriptFile(path, sessionID string) ([]SessionMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []transcriptEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.UUID == "" || !transcriptKeepTypes[e.Type] {
			continue
		}
		entries = append(entries, e)
	}
	chain := buildConversationChain(entries)
	msgs := make([]SessionMessage, 0, len(chain))
	for _, e := range chain {
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		if e.IsMeta || e.IsSidechain || e.TeamName != "" {
			continue
		}
		msgs = append(msgs, SessionMessage{
			Type:            e.Type,
			UUID:            e.UUID,
			SessionID:       e.SessionID,
			Message:         e.Message,
			ParentToolUseID: e.ParentToolUseID,
		})
	}
	return msgs, nil
}

// buildConversationChain reconstructs the chronological entry chain:
// terminals are entries never referenced as a parentUuid; among them
// prefer user/assistant non-sidechain non-team non-meta "main chain"
// candidates (latest in file order among ties), then walk backward via
// parentUuid -- never logicalParentUuid, so post-compaction history
// isn't double-counted.
func buildConversationChain(entries []transcriptEntry) []transcriptEntry {
	index := make(map[string]int, len(entries))
	for i := range entries {
		index[entries[i].UUID] = i
	}
	referenced := make(map[string]bool, len(entries))
	for i := range entries {
		if entries[i].ParentUUID != "" {
			referenced[entries[i].ParentUUID] = true
		}
	}
	var terminals []int
	for i := range entries {
		if !referenced[entries[i].UUID] {
			terminals = append(terminals, i)
		}
	}
	if len(terminals) == 0 {
		return nil
	}
	var candidates []int
	for _, i := range terminals {
		e := entries[i]
		if (e.Type == "user" || e.Type == "assistant") &&
			!e.IsSidechain && e.TeamName == "" && !e.IsMeta {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		candidates = terminals
	}
	// Terminals were collected in file order, so the last candidate is
	// the latest by file position.
	terminal := candidates[len(candidates)-1]

	var chain []transcriptEntry
	seen := make(map[string]bool)
	for i := terminal; i >= 0 && i < len(entries); {
		e := entries[i]
		if seen[e.UUID] {
			break
		}
		seen[e.UUID] = true
		chain = append(chain, e)
		if e.ParentUUID == "" {
			break
		}
		next, ok := index[e.ParentUUID]
		if !ok {
			break
		}
		i = next
	}
	for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
		chain[l], chain[r] = chain[r], chain[l]
	}
	return chain
}

// --- lite (head/tail) metadata extraction ---

// readLiteBuffers returns the first and last 64KB of the file (the whole
// file in both when it fits within 128KB total).
func readLiteBuffers(path string) (head, tail []byte, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, 0, err
	}
	size = st.Size()
	if size <= 2*liteReadChunk {
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, nil, 0, err
		}
		return data, data, size, nil
	}
	head = make([]byte, liteReadChunk)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, nil, 0, err
	}
	tail = make([]byte, liteReadChunk)
	if _, err := f.ReadAt(tail, size-liteReadChunk); err != nil {
		return nil, nil, 0, err
	}
	return head, tail, size, nil
}

func jsonFieldRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`"` + key + `":"((?:[^"\\]|\\.)*)"`)
}

// findJSONField scans buf for `"key":"value"` occurrences; last wins when
// lastWins, first when not. Substring scanning (not JSON parsing) because
// the head/tail buffers can be truncated mid-object.
func findJSONField(buf []byte, key string, lastWins bool) string {
	var found string
	for _, m := range jsonFieldRe(key).FindAllStringSubmatch(string(buf), -1) {
		found = m[1]
		if !lastWins {
			break
		}
	}
	if found == "" {
		return ""
	}
	return decodeJSONString(found)
}

func decodeJSONString(s string) string {
	var out string
	if json.Unmarshal([]byte(`"`+s+`"`), &out) == nil {
		return out
	}
	return s
}

var tagLinePrefix = []byte(`{"type":"tag"`)

// findTagField extracts tag only from lines starting with {"type":"tag" --
// an unrelated "tag" key (e.g. inside a Bash tool_use input) must not be
// trusted.
func findTagField(head, tail []byte) string {
	tag := ""
	for _, buf := range [][]byte{head, tail} {
		for _, line := range bytes.Split(buf, []byte("\n")) {
			if !bytes.HasPrefix(bytes.TrimSpace(line), tagLinePrefix) {
				continue
			}
			if v := findJSONField(line, "tag", true); v != "" {
				tag = v
			}
		}
	}
	return tag
}

// liteSessionInfo builds an SDKSessionInfo via the lite head/tail path.
// ok=false when the file is unreadable or nothing summary-like can be
// derived (sessions with no derivable summary, e.g. sidechains, are
// skipped rather than listed empty).
func liteSessionInfo(path, sessionID string) (SDKSessionInfo, bool) {
	head, tail, size, err := readLiteBuffers(path)
	if err != nil {
		return SDKSessionInfo{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return SDKSessionInfo{}, false
	}
	info := SDKSessionInfo{
		SessionID:    sessionID,
		LastModified: st.ModTime().UnixMilli(),
		FileSize:     &size,
	}
	// lastPrompt/summary/gitBranch/cwd are last-wins across both buffers.
	var lastPrompt, rawSummary, gitBranch, cwd string
	for _, buf := range [][]byte{head, tail} {
		if v := findJSONField(buf, "lastPrompt", true); v != "" {
			lastPrompt = v
		}
		if v := findJSONField(buf, "summary", true); v != "" {
			rawSummary = v
		}
		if v := findJSONField(buf, "gitBranch", true); v != "" {
			gitBranch = v
		}
		if v := findJSONField(buf, "cwd", true); v != "" {
			cwd = v
		}
	}
	// customTitle is last-wins and shadows aiTitle when present.
	var customTitle, aiTitle string
	for _, buf := range [][]byte{head, tail} {
		if v := findJSONField(buf, "customTitle", true); v != "" {
			customTitle = v
		}
		if v := findJSONField(buf, "aiTitle", true); v != "" {
			aiTitle = v
		}
	}
	info.CustomTitle = customTitle
	info.GitBranch = gitBranch
	info.Cwd = cwd
	info.Tag = findTagField(head, tail)

	// created_at: first successfully-parsed timestamp across the buffers.
	for _, buf := range [][]byte{head, tail} {
		ts := findJSONField(buf, "timestamp", false)
		if ts == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ms := t.UnixMilli()
			info.CreatedAt = &ms
			break
		}
	}

	info.FirstPrompt = firstPromptFromHead(head)
	// Summary fallback order: customTitle (or aiTitle) > lastPrompt >
	// raw summary field > first-prompt fallback.
	info.Summary = firstNonEmpty(customTitle, aiTitle, lastPrompt, rawSummary, info.FirstPrompt)
	if info.Summary == "" {
		return SDKSessionInfo{}, false
	}
	return info, true
}

// firstPromptFromHead scans the head buffer's lines for the first real
// user prompt: user-type, not tool_result-only, not isMeta, not
// isCompactSummary, not an auto-generated bracketed marker like
// <local-command-stdout> or <session-start-hook>.
func firstPromptFromHead(head []byte) string {
	for _, line := range bytes.Split(head, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("{")) {
			continue
		}
		var e struct {
			Type             string          `json:"type"`
			IsMeta           bool            `json:"isMeta"`
			IsCompactSummary bool            `json:"isCompactSummary"`
			Message          json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.IsMeta || e.IsCompactSummary {
			continue
		}
		if text := userMessageText(e.Message); text != "" && !autoMarkerRe.MatchString(text) {
			return truncatePrompt(text)
		}
	}
	return ""
}

// userMessageText extracts an entry's human-typed text: a string content
// field directly, or the concatenated text parts of an array content,
// skipping tool_result-only messages.
func userMessageText(msg json.RawMessage) string {
	if len(msg) == 0 {
		return ""
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []map[string]any
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if t, _ := p["type"].(string); t != "text" {
			continue
		}
		if text, _ := p["text"].(string); text != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(text)
		}
	}
	return sb.String()
}

func truncatePrompt(s string) string {
	if len(s) <= 200 {
		return s
	}
	// Cut on a rune boundary, not mid-byte.
	r := []rune(s)
	for len(r) > 0 && len(string(r))+1 > 200 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
