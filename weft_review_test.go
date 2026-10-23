package agentio_test

// Regression tests for the findings of the weft code review.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

// A CodedError wrapped with %w keeps the outer context and its own hint.
func TestWrappedCodedErrorKeepsOuterContext(t *testing.T) {
	err := fmt.Errorf("fetch inbox: %w", agentio.NewError("token expired", "mytool auth login"))
	line := agentio.FormatError("sync", err)
	want := "[error] sync: fetch inbox: token expired. Try: mytool auth login"
	if line != want {
		t.Fatalf("FormatError = %q, want %q", line, want)
	}
	if agentio.ExitCode(err) != agentio.ExitRuntime {
		t.Fatal("wrapped code lost")
	}
	direct := agentio.FormatError("sync", agentio.NewStaleError("token expired", "mytool auth login"))
	if direct != "[error] sync: token expired. Try: mytool auth login" {
		t.Fatalf("direct = %q", direct)
	}
}

// A typed nil *CodedError inside an error interface must not panic.
func TestTypedNilCodedErrorDoesNotPanic(t *testing.T) {
	var coded *agentio.CodedError
	var err error = coded
	if got := agentio.ExitCode(err); got != agentio.ExitRuntime {
		t.Fatalf("ExitCode = %d", got)
	}
	if line := agentio.FormatError("show", err); !strings.HasPrefix(line, "[error] show: ") {
		t.Fatalf("FormatError = %q", line)
	}
	a, _, stderr := newIO(t)
	if code := a.Fail(err); code != agentio.ExitRuntime || !strings.Contains(stderr.String(), "[error]") {
		t.Fatalf("Fail = %d, %q", code, stderr.String())
	}
	_ = coded.Error()
	_, _ = coded.What(), coded.Hint()
}

// When earlier output used part of the limit, the hint states the remaining limit.
func TestOverflowHintStatesTheRemainingLimit(t *testing.T) {
	dir := t.TempDir()
	a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(3))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "2 rows exceed the remaining limit of 1 (--limit 3); full result:") {
		t.Fatalf("hint = %q", stderr.String())
	}
}

// The override variable name is a valid shell name for any tool name.
func TestOutDirEnvironmentNameIsShellSafe(t *testing.T) {
	withTool(t, "my-tool.x")
	dir := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("MY_TOOL_X_OUT_DIR", dir)
	if got := agentio.OutDir(); got != dir {
		t.Fatalf("OutDir() = %q, want MY_TOOL_X_OUT_DIR to apply", got)
	}
}

// Characters outside the BMP are escaped with a valid 8-digit \U sequence.
func TestSupplementaryFormatCharactersUseLongEscapes(t *testing.T) {
	line := agentio.FormatError("show", errors.New("tag\U000E0041x"))
	if !strings.Contains(line, `\U000e0041`) || strings.Contains(line, "\U000E0041") {
		t.Fatalf("FormatError = %q, want \\U000e0041", line)
	}
}
