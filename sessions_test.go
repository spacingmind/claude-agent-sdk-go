package claudecode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeConfigHome points the session functions at a fresh temp directory
// and returns it, so tests never touch real env vars or $HOME.
func fakeConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := configHomeDir
	configHomeDir = func() string { return dir }

	t.Cleanup(func() { configHomeDir = prev })

	return dir
}

// fakeProject creates a real directory under the config home's parent
// (real, so sanitizeProjectDirName's EvalSymlinks works) plus its
// sanitized projects/<name> storage directory, returning both paths.
func fakeProject(t *testing.T, configHome, name string) (projectDir, storageDir string) {
	t.Helper()

	projectDir = filepath.Join(configHome, "workspaces", name)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	storageDir = filepath.Join(configHome, "projects", sanitizeProjectDirName(projectDir))
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	return projectDir, storageDir
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touchTime(t *testing.T, path string, at time.Time) {
	t.Helper()

	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func userEntry(uuid, parent, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%q,"sessionId":"sid","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, uuid, parent, text)
}

func assistantEntry(uuid, parent string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%q,"sessionId":"sid","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`, uuid, parent)
}

const (
	uuidA = "11111111-1111-1111-1111-111111111111"
	uuidB = "22222222-2222-2222-2222-222222222222"
	uuidC = "33333333-3333-3333-3333-333333333333"
	uuidD = "44444444-4444-4444-4444-444444444444"
	uuidE = "55555555-5555-5555-5555-555555555555"
	uuidF = "66666666-6666-6666-6666-666666666666"
)

func TestListSessions_SortLimitOffset(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	for i, sid := range []string{uuidA, uuidB, uuidC} {
		p := filepath.Join(storage, sid+".jsonl")
		writeLines(t, p, userEntry(sid, "", fmt.Sprintf("prompt %d", i)))
		touchTime(t, p, base.Add(time.Duration(i)*time.Hour))
	}

	got, err := ListSessions(ListSessionsOptions{Directory: filepath.Join(home, "workspaces", "proj")})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(got) != 3 || got[0].SessionID != uuidC || got[2].SessionID != uuidA {
		t.Fatalf("want newest-first %s,%s,%s; got %v", uuidC, uuidB, uuidA, got)
	}

	if got[0].Summary != "prompt 2" {
		t.Errorf("Summary = %q, want first-prompt fallback %q", got[0].Summary, "prompt 2")
	}

	limited, _ := ListSessions(ListSessionsOptions{Directory: filepath.Join(home, "workspaces", "proj"), Limit: 1})
	if len(limited) != 1 || limited[0].SessionID != uuidC {
		t.Errorf("Limit=1: got %v", limited)
	}

	paged, _ := ListSessions(ListSessionsOptions{Directory: filepath.Join(home, "workspaces", "proj"), Limit: 1, Offset: 1})
	if len(paged) != 1 || paged[0].SessionID != uuidB {
		t.Errorf("Offset=1,Limit=1: got %v", paged)
	}
}

func TestListSessions_AllProjectsWhenDirectoryEmpty(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage1 := fakeProject(t, home, "p1")
	writeLines(t, filepath.Join(storage1, uuidA+".jsonl"), userEntry(uuidA, "", "hello from p1"))
	_, storage2 := fakeProject(t, home, "p2")
	writeLines(t, filepath.Join(storage2, uuidB+".jsonl"), userEntry(uuidB, "", "hello from p2"))
	// Directory "" scans every projects/ subdirectory.
	got, err := ListSessions(ListSessionsOptions{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 sessions across both projects, got %d", len(got))
	}
}

func TestSanitize_LongPathHashRoundTrip(t *testing.T) {
	home := fakeConfigHome(t)
	// A deep chain whose sanitized name exceeds 200 chars.
	long := filepath.Join(home, "workspaces", strings.Repeat("deepdir/", 40)+"leaf")
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}

	sanitized := sanitizeProjectDirName(long)
	if len(sanitized) <= 200 {
		t.Fatalf("sanitized name %q not truncated", sanitized)
	}

	if !strings.HasPrefix(sanitized, sanitized[:200]+"-") {
		t.Fatalf("sanitized name %q lacks truncated prefix + '-'", sanitized)
	}

	storage := filepath.Join(home, "projects", sanitized)
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}

	writeLines(t, filepath.Join(storage, uuidA+".jsonl"), userEntry(uuidA, "", "deep"))

	got, err := GetSessionInfo(uuidA, long)
	if err != nil || got == nil {
		t.Fatalf("GetSessionInfo on long path: %v, %v", got, err)
	}

	if got.Summary != "deep" {
		t.Errorf("Summary = %q, want %q", got.Summary, "deep")
	}
}

func TestGetSessionMessages_CompactionBoundaryExcluded(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")
	// Pre-compaction chain u1->a1, then a compact_boundary pointing at a1
	// via logicalParentUuid (must NOT be followed), then the
	// isCompactSummary entry resuming the chain.
	writeLines(t, filepath.Join(storage, uuidA+".jsonl"),
		userEntry(uuidB, "", "old question"),
		assistantEntry(uuidC, uuidB),
		// compact_boundary's parentUuid is empty; its logicalParentUuid
		// points back at pre-compaction history that must NOT be
		// re-walked.
		fmt.Sprintf(`{"type":"system","uuid":%q,"parentUuid":"","logicalParentUuid":%q,"subtype":"compact_boundary"}`, uuidD, uuidC),
		fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%q,"isCompactSummary":true,"message":{"role":"user","content":[{"type":"text","text":"summary of earlier"}]}}`, uuidE, uuidD),
		userEntry(uuidF, uuidE, "new question"),
	)

	msgs, err := GetSessionMessages(uuidA, filepath.Join(home, "workspaces", "proj"), 0, 0)
	if err != nil {
		t.Fatalf("GetSessionMessages: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("want 2 visible messages (compact summary + new user), got %d: %+v", len(msgs), msgs)
	}

	for _, m := range msgs {
		if m.UUID == uuidB || m.UUID == uuidC {
			t.Errorf("pre-compaction entry %s double-counted", m.UUID)
		}
	}

	if msgs[0].UUID != uuidE {
		t.Errorf("first message = %s, want compact summary %s", msgs[0].UUID, uuidE)
	}
}

func TestGetSessionMessages_MetaSidechainExcluded(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")
	writeLines(t, filepath.Join(storage, uuidA+".jsonl"),
		fmt.Sprintf(`{"type":"user","uuid":%q,"isMeta":true,"message":{"role":"user","content":"meta"}}`, uuidB),
		fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%q,"isSidechain":true,"message":{"role":"user","content":"side"}}`, uuidC, uuidB),
		fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%q,"message":{"role":"assistant","content":"a"}}`, uuidD, uuidC),
	)

	msgs, err := GetSessionMessages(uuidA, filepath.Join(home, "workspaces", "proj"), 0, 0)
	if err != nil {
		t.Fatalf("GetSessionMessages: %v", err)
	}
	// The chain (via parentUuid) includes the sidechain entry, but the
	// visible filter drops isMeta/isSidechain entries.
	if len(msgs) != 1 || msgs[0].UUID != uuidD {
		t.Fatalf("want only %s visible, got %+v", uuidD, msgs)
	}
}

func TestSubagents_NestedWithMeta(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")
	// Session main file must exist for ID validation context.
	writeLines(t, filepath.Join(storage, uuidA+".jsonl"), userEntry(uuidA, "", "run subagent"))

	nested := filepath.Join(storage, uuidA, "subagents", "workflows", "run-1")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	writeLines(t, filepath.Join(nested, "agent-"+uuidB+".jsonl"),
		userEntry(uuidC, "", "sub prompt"),
		assistantEntry(uuidD, uuidC),
	)

	if err := os.WriteFile(filepath.Join(nested, "agent-"+uuidB+".meta.json"),
		[]byte(`{"toolUseId":"toolu_01","parentAgentId":"agent-xyz"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := ListSubagents(uuidA, filepath.Join(home, "workspaces", "proj"))
	if err != nil {
		t.Fatalf("ListSubagents: %v", err)
	}

	if len(ids) != 1 || ids[0] != uuidB {
		t.Fatalf("want agent id %s, got %v", uuidB, ids)
	}

	msgs, err := GetSubagentMessages(uuidA, uuidB, filepath.Join(home, "workspaces", "proj"), 0, 0)
	if err != nil {
		t.Fatalf("GetSubagentMessages: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("want 2 subagent messages, got %d", len(msgs))
	}

	if msgs[0].ParentToolUseID != "toolu_01" {
		t.Errorf("first ParentToolUseID = %q, want toolu_01", msgs[0].ParentToolUseID)
	}

	for i, m := range msgs {
		if m.ParentAgentID != "agent-xyz" {
			t.Errorf("msgs[%d].ParentAgentID = %q, want agent-xyz", i, m.ParentAgentID)
		}
	}
}

func TestSessionInfo_TitleFallbackPriority(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")

	cases := []struct {
		name string
		line string
		want string
	}{
		{"customTitle wins", `{"type":"summary","customTitle":"Custom","aiTitle":"AI","lastPrompt":"LP","summary":"S"}`, "Custom"},
		{"aiTitle", `{"type":"summary","aiTitle":"AI","lastPrompt":"LP","summary":"S"}`, "AI"},
		{"lastPrompt", `{"type":"summary","lastPrompt":"LP","summary":"S"}`, "LP"},
		{"raw summary", `{"type":"summary","summary":"S"}`, "S"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(storage, uuidA+".jsonl")
			writeLines(t, p, userEntry(uuidA, "", "first prompt"), tc.line)

			info, err := GetSessionInfo(uuidA, filepath.Join(home, "workspaces", "proj"))
			if err != nil || info == nil {
				t.Fatalf("GetSessionInfo: %v, %v", info, err)
			}

			if info.Summary != tc.want {
				t.Errorf("Summary = %q, want %q", info.Summary, tc.want)
			}
		})
	}
}

func TestSessions_CorruptLineResilience(t *testing.T) {
	home := fakeConfigHome(t)
	_, storage := fakeProject(t, home, "proj")
	p := filepath.Join(storage, uuidA+".jsonl")
	writeLines(t, p,
		userEntry(uuidB, "", "real prompt"),
		`{"type":"assistant","uuid":`, // corrupt line
		assistantEntry(uuidC, uuidB),
	)

	msgs, err := GetSessionMessages(uuidA, filepath.Join(home, "workspaces", "proj"), 0, 0)
	if err != nil {
		t.Fatalf("GetSessionMessages: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("want 2 messages despite corrupt line, got %d", len(msgs))
	}
	// Listing also survives: the session still appears.
	got, err := ListSessions(ListSessionsOptions{Directory: filepath.Join(home, "workspaces", "proj")})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("want 1 session listed despite corrupt line, got %d", len(got))
	}
}

func TestSessions_EmptyAndMissingDirectories(t *testing.T) {
	home := fakeConfigHome(t)
	// Empty projects dir.
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ListSessions(ListSessionsOptions{})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty projects dir: got %v, %v; want empty, nil", got, err)
	}
	// Nonexistent config home entirely.
	home2 := fakeConfigHome(t)
	_ = home2

	got2, err := ListSessions(ListSessionsOptions{Directory: "/nonexistent/project"})
	if err != nil || len(got2) != 0 {
		t.Fatalf("nonexistent dir: got %v, %v; want empty, nil", got2, err)
	}

	info, err := GetSessionInfo(uuidA, "/nonexistent/project")
	if err != nil || info != nil {
		t.Fatalf("GetSessionInfo nonexistent: got %v, %v; want nil, nil", info, err)
	}
}

func TestGetSessionMessages_InvalidSessionIDRejected(t *testing.T) {
	home := fakeConfigHome(t)

	_, _ = fakeProject(t, home, "proj")
	if _, err := GetSessionMessages("not-a-uuid", filepath.Join(home, "workspaces", "proj"), 0, 0); err == nil {
		t.Fatal("non-UUID session ID must return an error")
	}
}

func TestGetSessionMessages_MissingSessionReturnsNil(t *testing.T) {
	home := fakeConfigHome(t)
	_, _ = fakeProject(t, home, "proj")

	msgs, err := GetSessionMessages(uuidA, filepath.Join(home, "workspaces", "proj"), 0, 0)
	if err != nil || msgs != nil {
		t.Fatalf("missing session: got %v, %v; want nil, nil", msgs, err)
	}
}

func TestTruncatePrompt(t *testing.T) {
	t.Run("short string unchanged", func(t *testing.T) {
		if got := truncatePrompt("hello"); got != "hello" {
			t.Errorf("truncatePrompt(short) = %q, want unchanged", got)
		}
	})

	t.Run("exactly 200 runes unchanged", func(t *testing.T) {
		s := strings.Repeat("a", 200)
		if got := truncatePrompt(s); got != s {
			t.Errorf("truncatePrompt(200 runes) modified, want unchanged")
		}
	})

	t.Run("long string truncates with ellipsis", func(t *testing.T) {
		s := strings.Repeat("a", 250)
		got := truncatePrompt(s)

		if !strings.HasSuffix(got, "…") {
			t.Errorf("truncatePrompt(long) = %q, want trailing ellipsis", got)
		}

		if n := len([]rune(got)); n != 200 {
			t.Errorf("truncatePrompt(long) = %d runes (incl. ellipsis), want 200", n)
		}
	})

	t.Run("truncates on rune boundary, not mid-byte", func(t *testing.T) {
		// Multi-byte runes near the cut point must not be split.
		s := strings.Repeat("é", 250) // each 'é' is 2 bytes in UTF-8
		got := truncatePrompt(s)

		if !utf8.ValidString(got) {
			t.Errorf("truncatePrompt produced invalid UTF-8: %q", got)
		}
	})
}

func TestWorktreeProjectDirs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	home := fakeConfigHome(t)

	main := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...) //nolint:gosec,noctx  // fixed test-internal args, not user input; no context needed in a test helper
		cmd.Dir = main

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-q")
	runGit("-c", "user.email=t@t.com", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init")

	worktreeDir := t.TempDir() + "-wt"
	runGit("worktree", "add", "-q", worktreeDir, "-b", "feature")

	// worktreeProjectDirs resolves each worktree path to its session-storage
	// directory under <config_home>/projects, mirroring resolveProjectDir --
	// it never returns the raw filesystem path, so the fixture must have a
	// matching sanitized storage dir for the lookup to succeed.
	wantStorage := filepath.Join(home, "projects", sanitizeProjectDirName(worktreeDir))
	if err := os.MkdirAll(wantStorage, 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := worktreeProjectDirs(main)

	found := false

	for _, d := range dirs {
		if d == wantStorage {
			found = true
		}
	}

	if !found {
		t.Errorf("worktreeProjectDirs(%q) = %v, want to include %q", main, dirs, wantStorage)
	}
}

func TestWorktreeProjectDirs_NotAGitRepo(t *testing.T) {
	if got := worktreeProjectDirs(t.TempDir()); got != nil {
		t.Errorf("worktreeProjectDirs(non-repo) = %v, want nil", got)
	}
}

func TestReadLiteBuffers_LargeFileSplitsHeadTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")

	const chunk = 64 * 1024

	data := make([]byte, 3*chunk)
	for i := range data {
		data[i] = byte('a' + i%26)
	}

	copy(data[:4], []byte("HEAD"))
	copy(data[len(data)-4:], []byte("TAIL"))

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	head, tail, size, err := readLiteBuffers(path)
	if err != nil {
		t.Fatalf("readLiteBuffers: %v", err)
	}

	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}

	if len(head) != chunk || string(head[:4]) != "HEAD" {
		t.Errorf("head = %d bytes starting %q, want %d bytes starting HEAD", len(head), head[:4], chunk)
	}

	if len(tail) != chunk || string(tail[len(tail)-4:]) != "TAIL" {
		t.Errorf("tail = %d bytes ending %q, want %d bytes ending TAIL", len(tail), tail[len(tail)-4:], chunk)
	}
}

func TestReadLiteBuffers_MissingFile(t *testing.T) {
	_, _, _, err := readLiteBuffers(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err == nil {
		t.Fatal("readLiteBuffers(missing file) = nil error, want error")
	}
}
