package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
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

	closeOnce sync.Once
}

func startTransport(worktreePath, cliPath string, args, env []string, stderr io.Writer) (*transport, error) {
	cmd := exec.Command(cliPath, args...)
	cmd.Dir = worktreePath
	if env != nil {
		cmd.Env = env
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudecode: start %s: %w", cliPath, err)
	}

	t := &transport{
		cmd:    cmd,
		stdin:  stdin,
		lines:  make(chan lineResult),
		closed: make(chan struct{}),
	}
	go t.readLoop(stdout)
	return t, nil
}

func (t *transport) readLoop(stdout io.Reader) {
	defer close(t.lines)

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
// waits up to gracePeriod for it to do so, and force-kills it otherwise. An
// error from Wait after a forced kill is expected (the process died by
// signal, not by choice) and is not reported; an error from a CLI that
// exited badly on its own within the grace period is reported.
func (t *transport) close(gracePeriod time.Duration) error {
	var closeErr error
	t.closeOnce.Do(func() {
		close(t.closed)
		_ = t.stdin.Close()

		done := make(chan error, 1)
		go func() { done <- t.cmd.Wait() }()

		select {
		case err := <-done:
			closeErr = err
		case <-time.After(gracePeriod):
			_ = t.cmd.Process.Kill()
			<-done
		}
	})
	return closeErr
}
