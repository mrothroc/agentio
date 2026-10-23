package agentio_test

// Regression tests for the second adversarial review round.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

// A trailing separator made Lstat follow the symlink, bypassing the check.
func TestTrailingSlashDoesNotBypassSymlinkRefusal(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, dir := range []string{link + "/", link + "//", link + "/."} {
		a, _, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil {
			t.Errorf("%q accepted", dir)
		}
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v", entries)
	}
}

// Slices that share a backing array without containing themselves are valid.
func TestSharedSliceStorageIsNotACycle(t *testing.T) {
	v := make([]any, 2)
	v[0] = "ok"
	v[1] = v[:1]
	if _, err := agentio.RedactValue(v); err != nil {
		t.Fatalf("acyclic shared storage rejected: %v", err)
	}
	c := make([]any, 1)
	c[0] = c
	if _, err := agentio.RedactValue(c); err == nil {
		t.Fatal("slice cycle not rejected")
	}
}

// hintPaths returns the single-quoted shell words from the runnable hints.
func runnableHints(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "[hint] inspect with: ") || strings.HasPrefix(line, "[hint] or: ") {
			out = append(out, line)
		}
	}
	return out
}

// shellWord evaluates the last quoted path of a hint with a real shell.
func shellPathOf(t *testing.T, hint string) string {
	t.Helper()
	i := strings.Index(hint, "-- ")
	if i < 0 {
		t.Fatalf("no -- in hint %q", hint)
	}
	word := strings.TrimSpace(hint[i+3:])
	if j := strings.Index(word, " |"); j >= 0 {
		word = word[:j]
	}
	out, err := exec.Command("sh", "-c", "printf %s "+word).Output()
	if err != nil {
		t.Fatalf("shell rejected %q: %v", word, err)
	}
	return string(out)
}

// The shell must receive exactly the overflow path, whatever it contains.
func TestRunnableHintsNameTheExactPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's $(id) `x`")
	a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	hints := runnableHints(stderr.String())
	if len(hints) != 2 {
		t.Fatalf("hints %q", stderr.String())
	}
	entries, _ := os.ReadDir(dir)
	want := filepath.Join(dir, entries[0].Name())
	for _, h := range hints {
		if got := shellPathOf(t, h); got != want {
			t.Fatalf("hint %q names %q, want %q", h, got, want)
		}
	}
	if !strings.Contains(stderr.String(), "grep -e '<pattern>' -- ") {
		t.Fatalf("pattern placeholder not quoted: %s", stderr.String())
	}
}

// When the path cannot be shown exactly (control characters, or a secret that
// must be redacted), no runnable command is suggested.
func TestNoRunnableHintWhenThePathCannotBeShownExactly(t *testing.T) {
	withSecret(t, secret)
	for name, dir := range map[string]string{
		"control": filepath.Join(t.TempDir(), "tab\there"),
		"secret":  filepath.Join(t.TempDir(), "out-"+secret),
	} {
		a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
			t.Fatal(err)
		}
		if h := runnableHints(stderr.String()); len(h) != 0 {
			t.Errorf("%s: runnable hint for an inexact path: %q", name, h)
		}
		if !strings.Contains(stderr.String(), "full result:") || strings.Contains(stderr.String(), secret) || strings.Contains(stderr.String(), "\t") {
			t.Errorf("%s: hint %q", name, stderr.String())
		}
	}
}

type failAfter struct {
	writes int
	n      int
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n >= f.writes {
		return 0, errors.New("disk full")
	}
	f.n++
	return len(p), nil
}

// Rows that reached stdout before a write failure stay charged, and nothing
// more is written in the invocation (see TestStdoutFailureEndsOutputForTheInvocation).
func TestPartialWritesAreChargedToTheLimit(t *testing.T) {
	dir := t.TempDir()
	a := agentio.New("demo", agentio.WithStdout(&failAfter{writes: 2}), agentio.WithStderr(&strings.Builder{}),
		agentio.WithOverflowDir(dir), agentio.WithLimit(2))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil {
		t.Fatal("write failure not reported")
	}
	var out strings.Builder
	a.SetStdout(&out)
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil || out.Len() != 0 {
		t.Fatalf("output resumed after a failure: %q (%v)", out.String(), err)
	}
	a.Reset()
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil || strings.Count(out.String(), "\n") != 3 {
		t.Fatalf("Reset did not start a fresh run: %q (%v)", out.String(), err)
	}
}

// Unicode line separators and bidi controls must not survive into diagnostics.
func TestDiagnosticsEscapeUnicodeSeparatorsAndBidi(t *testing.T) {
	for _, in := range []string{"a [error] forged", "a b", "a\u0085b", "a‮b", "a⁦b"} {
		line := agentio.FormatError("show", errors.New(in))
		if strings.ContainsAny(line, "  \u0085‮⁦") {
			t.Errorf("FormatError kept a separator or bidi control: %q", line)
		}
	}
}
