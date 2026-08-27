package claudecode

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// minimumCLIVersion is the advisory floor for the version check run at
// New() time (matching the Python reference). Below it, a one-line
// warning is logged; nothing is blocked.
const minimumCLIVersion = "2.0.0"

// cliFallbackPaths returns the well-known install locations probed when
// `claude` is not on PATH. A function variable (not a direct value) so
// tests can point the probe list at temp directories instead of touching
// real $HOME (the same seam pattern as configHomeDir).
var cliFallbackPaths = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	var out []string

	for _, p := range []string{
		"~/.npm-global/bin/claude",
		"/usr/local/bin/claude",
		"~/.local/bin/claude",
		"~/node_modules/.bin/claude",
		"~/.yarn/bin/claude",
		"~/.claude/local/claude",
	} {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			if home == "" {
				continue
			}

			p = filepath.Join(home, rest)
		}

		out = append(out, p)
	}

	return out
}

// resolveCLIPath picks the claude binary path for New: an explicit
// WithCLIPath value passes through unchanged (no discovery, no extra
// validation, matching pre-discovery behavior); otherwise PATH is
// searched first, then the well-known fallback locations, and a miss
// everywhere yields an actionable CLINotFoundError.
func resolveCLIPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}

	fallbacks := cliFallbackPaths()
	for _, p := range fallbacks {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}

	return "", &CLINotFoundError{
		Path: "claude",
		Err: fmt.Errorf("not found on PATH or in any of the fallback locations (%s); install it with `npm install -g @anthropic-ai/claude-code` or point at it explicitly with WithCLIPath",
			strings.Join(fallbacks, ", ")),
	}
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// checkCLIVersion is the advisory version probe New runs in a goroutine:
// it spawns `<cliPath> -v` bounded by a 2-second context, parses the first
// semver-ish substring from stdout, and writes one warning line to logWriter
// when the version is below minimumCLIVersion. Every failure (spawn error,
// timeout, unparseable output) is silently swallowed, and a nil logWriter
// skips the probe entirely -- it must never block or fail construction.
func checkCLIVersion(cliPath string, env []string, logWriter io.Writer) {
	if logWriter == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-v") //nolint:gosec  // cliPath resolved/validated at New time; bounded by ctx
	if env != nil {
		cmd.Env = env
	}

	out, err := cmd.Output()
	if err != nil {
		return
	}

	version := versionRe.FindString(string(out))
	if version == "" {
		return
	}

	if compareVersions(version, minimumCLIVersion) < 0 {
		_, _ = fmt.Fprintf(logWriter, "claudecode: claude CLI version %s is below the recommended minimum %s\n", version, minimumCLIVersion)
	}
}

// compareVersions compares two dotted three-component-ish version strings
// component by component (missing components count as 0), returning -1, 0,
// or 1. Unparseable components count as 0 rather than erroring -- this is
// an advisory check, not a gate.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")

	for i := range max(len(as), len(bs)) {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}

		if av != bv {
			if av < bv {
				return -1
			}

			return 1
		}
	}

	return 0
}
