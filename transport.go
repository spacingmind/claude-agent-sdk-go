package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxLineBytes bounds a single NDJSON line from the CLI, matching the
// Python SDK's default transport buffer limit.
const maxLineBytes = 1024 * 1024

type lineResult struct {
	data []byte
	err  error
}

// transport owns one claude subprocess's stdin/stdout and the background
// goroutine that frames its NDJSON stdout into lines on the lines channel.
//
// Claude Code reads and writes the filesystem directly through its own
// working directory (cmd.Dir), unlike an ACP agent which delegates file
// access back to the client over the wire -- so this transport carries no
// filesystem callback plumbing, only the message stream and the
// control-request/response handshake built on top of it.
type transport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	lines  chan lineResult
	closed chan struct{}

	// readDone closes when the stdout reader goroutine has finished (EOF,
	// scan error, or transport close). The waiter goroutine blocks on it
	// before calling cmd.Wait, because Wait closes the stdout pipe and
	// "it is incorrect to call Wait before all reads from the pipe have
	// completed": reaping early discards stdout bytes the CLI wrote but we
	// had not read yet.
	readDone chan struct{}

	// stderrTail keeps the last bytes of the CLI's stderr for ProcessError.
	stderrTail *tailBuffer

	// waitDone closes once the single waiter goroutine has reaped the
	// subprocess and published waitErr and status (written before the
	// close, so any reader after <-waitDone sees them without further
	// synchronization). cmd.Wait also waits for the stderr copier to see
	// EOF (bounded by cmd.WaitDelay), so stderrTail is complete by then.
	waitDone chan struct{}
	waitErr  error
	status   ExitStatus

	closeOnce sync.Once
}

// buildEnv assembles the CLI subprocess environment: the inherited
// environment minus CLAUDECODE (so SDK-spawned subprocesses never think
// they run inside a Claude Code parent), then CLAUDE_CODE_ENTRYPOINT=sdk-go
// unless the caller's vars already set that key (caller override wins),
// then the caller-supplied vars on top. Mirrors the Python SDK's connect().
func buildEnv(caller []string) []string {
	entrypointSet := false

	for _, kv := range caller {
		if strings.HasPrefix(kv, "CLAUDE_CODE_ENTRYPOINT=") {
			entrypointSet = true
			break
		}
	}

	inherited := os.Environ()

	env := make([]string, 0, len(inherited)+len(caller)+1)
	for _, kv := range inherited {
		if kv == "CLAUDECODE" || strings.HasPrefix(kv, "CLAUDECODE=") {
			continue
		}

		env = append(env, kv)
	}

	if !entrypointSet {
		env = append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-go")
	}

	return append(env, caller...)
}

func startTransport(worktreePath, cliPath string, args, env []string, stderr io.Writer) (*transport, error) {
	//nolint:gosec,noctx  // spawning the CLI is the package's purpose; cliPath is caller-supplied, and lifecycle is owned by transport.close (stdin-close -> grace -> kill), not a context
	cmd := exec.Command(cliPath, args...)

	cmd.Dir = worktreePath
	if env != nil {
		cmd.Env = env
	}

	// Always capture stderr (tee'd to the caller's writer) so ProcessError
	// can carry its tail. A non-*os.File Stderr makes exec run a copier
	// goroutine that cmd.Wait joins, so stderr is drained before the exit
	// status is published; WaitDelay bounds that join if a grandchild
	// keeps the pipe open.
	tail := &tailBuffer{max: stderrTailBytes}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, tail)
	} else {
		cmd.Stderr = tail
	}

	cmd.WaitDelay = stderrDrainTimeout

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &CLIConnectionError{Err: fmt.Errorf("stdin pipe: %w", err)}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &CLIConnectionError{Err: fmt.Errorf("stdout pipe: %w", err)}
	}

	if err := cmd.Start(); err != nil {
		if isNotFoundErr(err) {
			return nil, &CLINotFoundError{Path: cliPath, Err: err}
		}

		return nil, &CLIConnectionError{Err: fmt.Errorf("start %s: %w", cliPath, err)}
	}

	t := &transport{
		cmd:      cmd,
		stdin:    stdin,
		lines:    make(chan lineResult),
		closed:   make(chan struct{}),
		readDone: make(chan struct{}),
		waitDone: make(chan struct{}),

		stderrTail: tail,
	}

	t.status = ExitStatus{Code: -1}
	go t.readLoop(stdout)
	go t.wait()

	return t, nil
}

// isNotFoundErr reports whether a cmd.Start failure means the binary itself
// could not be found or executed: an exec.ErrNotFound in the chain, or a
// syscall-level not-exist/permission-denied on the resolved path.
func isNotFoundErr(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}

	if pathErr, ok := errors.AsType[*os.PathError](err); ok {
		return errors.Is(pathErr.Err, os.ErrNotExist) || errors.Is(pathErr.Err, os.ErrPermission)
	}

	return false
}

// exited reports whether the subprocess has been reaped yet, race-free
// (cmd.ProcessState must only be touched by the waiter goroutine).
func (t *transport) exited() bool {
	select {
	case <-t.waitDone:
		return true
	default:
		return false
	}
}

// exitStatus returns the subprocess's exit status, waiting up to timeout
// for the waiter goroutine to reap it. ok is false (and Code is -1) if the
// process had not been reaped by then.
func (t *transport) exitStatus(timeout time.Duration) (ExitStatus, bool) {
	select {
	case <-t.waitDone:
		return t.status, true
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-t.waitDone:
		return t.status, true
	case <-timer.C:
		return ExitStatus{Code: -1}, false
	}
}

// wait is the single reaper for the subprocess: exactly one goroutine may
// call cmd.Wait. It first waits for the stdout reader to finish (or the
// transport to be closed) so Wait cannot close the pipe under unread data,
// then publishes the wait error and exit status so close and ProcessError
// construction can consume them without racing.
func (t *transport) wait() {
	select {
	case <-t.readDone:
	case <-t.closed:
	}

	t.waitErr = t.cmd.Wait()
	if errors.Is(t.waitErr, exec.ErrWaitDelay) {
		// The process itself exited; only a lingering pipe holder was cut
		// off. Not a process failure.
		t.waitErr = nil
	}

	t.status = ExitStatus{Code: -1}

	if ps := t.cmd.ProcessState; ps != nil {
		t.status.Code = ps.ExitCode()

		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			t.status.Signal = ws.Signal()
		}
	}

	t.status.Stderr = t.stderrTail.String()

	close(t.waitDone)
}

func (t *transport) readLoop(stdout io.Reader) {
	defer close(t.lines)
	defer close(t.readDone)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		select {
		case t.lines <- lineResult{data: line}:
		case <-t.closed:
			return
		}
	}

	if err := scanner.Err(); err != nil {
		select {
		case t.lines <- lineResult{err: err}:
		case <-t.closed:
		}
	}
}

func (t *transport) writeLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("claudecode: marshal: %w", err)
	}

	data = append(data, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("claudecode: write stdin: %w", err)
	}

	return nil
}

// close closes stdin (so a well-behaved CLI can flush and exit on its own),
// then escalates: wait up to gracePeriod for a natural exit, SIGTERM and
// wait another gracePeriod, and finally SIGKILL -- a total worst case of
// roughly 3x gracePeriod. An error from Wait after we ourselves signaled the
// process to die (SIGTERM or SIGKILL) is expected and not reported; only an
// error from a CLI that exited badly entirely on its own, before any signal
// was sent, is reported.
func (t *transport) close(gracePeriod time.Duration) error {
	var closeErr error

	t.closeOnce.Do(func() {
		close(t.closed)
		_ = t.stdin.Close()

		// Stage 1: natural exit on stdin EOF.
		select {
		case <-t.waitDone:
			closeErr = t.waitErr
			return
		case <-time.After(gracePeriod):
		}

		// Stage 2: SIGTERM. Like the SIGKILL stage below, a Wait error here
		// is expected (we're the ones who signaled it to die) and is not
		// reported.
		_ = t.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-t.waitDone:
			return
		case <-time.After(gracePeriod):
		}

		// Stage 3: SIGKILL. An error from Wait after a forced kill is
		// expected and suppressed (see the doc comment).
		_ = t.cmd.Process.Kill()
		<-t.waitDone
	})

	return closeErr
}

const (
	// stderrTailBytes bounds the stderr tail kept for ProcessError.
	stderrTailBytes = 8 * 1024

	// stderrDrainTimeout bounds how long cmd.Wait waits for the stderr
	// copier after the process exits.
	stderrDrainTimeout = 2 * time.Second

	// exitStatusWait bounds how long the Client waits for the process to be
	// reaped after the stdout stream ends, before reporting an unknown
	// exit status.
	exitStatusWait = 5 * time.Second
)

// tailBuffer is an io.Writer retaining only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}

	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return strings.TrimSpace(string(b.buf))
}
