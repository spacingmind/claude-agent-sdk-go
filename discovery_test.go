package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// lockedBuffer is a concurrency-safe io.Writer: the version probe writes
// from its own goroutine (like the CLI's stderr copier), so a plain
// strings.Builder would race with the test's reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// withCLIStubHome points the fallback-path probe at a temp home so tests
// never touch the real $HOME or $PATH.
func withCLIStubHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	old := cliFallbackPaths
	cliFallbackPaths = func() []string {
		return []string{
			filepath.Join(home, ".npm-global", "bin", "claude"),
			filepath.Join(home, ".local", "bin", "claude"),
		}
	}

	t.Cleanup(func() { cliFallbackPaths = old })

	return home
}

func writeStubCLI(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(self, path); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCLIPath_FallbackDiscovery(t *testing.T) {
	home := withCLIStubHome(t)
	t.Setenv("PATH", "")

	stub := filepath.Join(home, ".npm-global", "bin", "claude")
	writeStubCLI(t, stub)

	got, err := resolveCLIPath("")
	if err != nil {
		t.Fatalf("resolveCLIPath() error = %v", err)
	}

	if got != stub {
		t.Fatalf("resolveCLIPath() = %q, want %q", got, stub)
	}
}

func TestResolveCLIPath_ExplicitPassthrough(t *testing.T) {
	t.Setenv("PATH", "")

	got, err := resolveCLIPath("/definitely/not/checked")
	if err != nil || got != "/definitely/not/checked" {
		t.Fatalf("resolveCLIPath(explicit) = %q, %v; want passthrough", got, err)
	}
}

func TestResolveCLIPath_NotFoundActionable(t *testing.T) {
	withCLIStubHome(t)
	t.Setenv("PATH", "")

	_, err := resolveCLIPath("")

	var nf *CLINotFoundError

	if err == nil {
		t.Fatal("resolveCLIPath() succeeded, want error")
	}

	e := &CLINotFoundError{}
	if errors.As(err, &e) {
		nf = e
	} else {
		t.Fatalf("error type = %T, want *CLINotFoundError", err)
	}

	if nf.Path != "claude" {
		t.Fatalf("Path = %q, want %q", nf.Path, "claude")
	}

	msg := nf.Error()
	for _, want := range []string{"npm install -g @anthropic-ai/claude-code", "WithCLIPath", ".npm-global"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestNew_FailsWhenCLINotFound(t *testing.T) {
	withCLIStubHome(t)
	t.Setenv("PATH", "")

	_, err := New(t.TempDir())

	var nf *CLINotFoundError

	if err == nil {
		t.Fatal("New() succeeded, want CLINotFoundError")
	}

	e := &CLINotFoundError{}
	if !errors.As(err, &e) {
		t.Fatalf("New() error = %T, want *CLINotFoundError", err)
	} else {
		nf = e
	}

	_ = nf
}

func TestNew_DiscoveryFallbackSpawnsStub(t *testing.T) {
	home := withCLIStubHome(t)
	t.Setenv("PATH", "")

	stub := filepath.Join(home, ".local", "bin", "claude")
	writeStubCLI(t, stub)

	env := append(os.Environ(), "CLAUDECODE_FAKE_CLI=1", "CLAUDECODE_FAKE_SCENARIO=malformed")

	c, err := New(t.TempDir(), withExtraEnv(env), WithLogWriter(&lockedBuffer{}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	defer func() { _ = c.Close() }()

	if got := c.tr.cmd.Path; got != stub {
		t.Errorf("spawned %q, want %q", got, stub)
	}
}

func TestClient_CloseSigtermStage(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "sigterm_exit"), withCloseGracePeriod(200*time.Millisecond))

	start := time.Now()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	elapsed := time.Since(start)
	// Natural-exit stage (200ms) + SIGTERM stage (<200ms) => well under 3
	// grace periods; a SIGKILL path would take ~600ms.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Close() took %v, want SIGTERM exit well before the SIGKILL stage", elapsed)
	}

	if err := syscall.Kill(c.tr.cmd.Process.Pid, 0); err == nil {
		t.Fatal("process still alive after Close()")
	}
}

func TestClient_CloseSigkillStage(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "ignore_signals"), withCloseGracePeriod(200*time.Millisecond))

	start := time.Now()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil (post-SIGKILL wait errors suppressed)", err)
	}

	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("Close() took %v, want ~3 grace periods (roughly 600ms)", elapsed)
	}

	if err := syscall.Kill(c.tr.cmd.Process.Pid, 0); err == nil {
		t.Fatal("process still alive after Close()")
	}
}

func TestClient_VersionCheckWarnsBelowMinimum(t *testing.T) {
	t.Parallel()

	var log lockedBuffer

	env := append(os.Environ(),
		"CLAUDECODE_FAKE_CLI=1",
		"CLAUDECODE_FAKE_SCENARIO=malformed",
		"CLAUDECODE_FAKE_VERSION=1.0.3",
	)

	self, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatal(exeErr)
	}

	opts := []Option{WithCLIPath(self), withExtraEnv(env), WithLogWriter(&log)}

	start := time.Now()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	defer func() { _ = c.Close() }()

	if time.Since(start) > 2*time.Second {
		t.Fatal("New() blocked on the version probe")
	}

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(log.String(), "below the recommended minimum") {
		if time.Now().After(deadline) {
			t.Fatalf("version warning never logged, got: %q", log.String())
		}

		time.Sleep(10 * time.Millisecond)
	}

	if !strings.Contains(log.String(), "1.0.3") || !strings.Contains(log.String(), minimumCLIVersion) {
		t.Fatalf("warning missing versions: %q", log.String())
	}
}

func TestClient_VersionCheckNoLogWriter(t *testing.T) {
	t.Parallel()

	env := append(os.Environ(),
		"CLAUDECODE_FAKE_CLI=1",
		"CLAUDECODE_FAKE_SCENARIO=malformed",
		"CLAUDECODE_FAKE_VERSION=1.0.3",
	)

	self, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatal(exeErr)
	}

	opts := []Option{WithCLIPath(self), withExtraEnv(env)}

	start := time.Now()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	defer func() { _ = c.Close() }()

	if time.Since(start) > 2*time.Second {
		t.Fatal("New() blocked on the version probe")
	}
}

func TestClient_VersionCheckHangingProbeDoesNotBlock(t *testing.T) {
	t.Parallel()

	env := append(os.Environ(),
		"CLAUDECODE_FAKE_CLI=1",
		"CLAUDECODE_FAKE_SCENARIO=malformed",
		"CLAUDECODE_FAKE_VERSION=hang",
	)

	self, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatal(exeErr)
	}

	opts := []Option{WithCLIPath(self), withExtraEnv(env), WithLogWriter(&lockedBuffer{})}

	start := time.Now()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	defer func() { _ = c.Close() }()

	// The probe sleeps an hour; New must still return immediately (the
	// 2s context reaps it in the background).
	if time.Since(start) > 1*time.Second {
		t.Fatalf("New() took %v, want it unblocked by the hanging -v probe", time.Since(start))
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "2.0.0", 0},
		{"2.1.0", "2.0.9", 1},
		{"2.0.10", "2.0.9", 1},
		{"1.99.99", "2.0.0", -1},
		{"2.0", "2.0.0", 0},
		{"x.y.z", "0.0.0", 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
