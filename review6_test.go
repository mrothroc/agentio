package agentio_test

// Regression tests for the sixth adversarial review round.

import (
	"os"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

func TestApostropheWrappedURLsNeverLeakCredentials(t *testing.T) {
	agentio.ResetSecrets()
	for in, want := range map[string]string{
		"request failed for 'https://user:pa'ssword@host/x'": "request failed for 'https://[REDACTED]@host/x'",
		"'https://o'brien:password@host'":                    "'https://[REDACTED]@host'",
		"'//o'brien:password@host'":                          "'//[REDACTED]@host'",
	} {
		if got := agentio.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDoubleSlashInsideAURLPathIsNotAnAuthority(t *testing.T) {
	agentio.ResetSecrets()
	for _, keep := range []string{
		"https://example.org/files//alice@example.net",
		"/var/data//bob@host",
	} {
		if got := agentio.Redact(keep); got != keep {
			t.Errorf("Redact(%q) = %q, want unchanged", keep, got)
		}
	}
	for in, want := range map[string]string{
		"url=//u:p@h":          "url=//[REDACTED]@h",
		"(//u:p@h)":            "(//[REDACTED]@h)",
		"git+ssh://git@host/r": "git+ssh://[REDACTED]@host/r",
	} {
		if got := agentio.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// Errors from the overflow path must not show a cleaned directory, which can
// no longer match a registered spelling.
func TestOverflowErrorsUseTheConfiguredDirectory(t *testing.T) {
	withSecret(t, "private-token/.")
	base := t.TempDir()
	out := base + "/private-token/out"
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o777); err != nil { // refused: writable by others
		t.Fatal(err)
	}
	a, _, stderr := newIO(t, agentio.WithOverflowDir(base+"/private-token/./out"), agentio.WithLimit(1))
	err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj)
	if err == nil {
		t.Fatal("unsafe directory accepted")
	}
	a.Fail(err)
	if strings.Contains(stderr.String(), "private-token") {
		t.Fatalf("cleaned path leaked through the error: %q", stderr.String())
	}
}

func TestSetToolRejectsSurroundingWhitespace(t *testing.T) {
	withTool(t, "samplecli")
	mustPanic(t, "padded tool name", func() { agentio.SetTool(" private-tool ") })
}

func TestFailfRedactsTheFormatString(t *testing.T) {
	withSecret(t, "hunter%%2")
	a, _, stderr := newIO(t)
	a.Failf("", "hunter%%2")
	if strings.Contains(stderr.String(), "hunter") {
		t.Fatalf("format-string secret leaked: %q", stderr.String())
	}
}

// The cache-directory fallback keeps its raw spelling for redaction decisions.
func TestCacheFallbackHintsUseTheRawSpelling(t *testing.T) {
	withTool(t, "samplecli")
	withSecret(t, "private-token/.")
	agentio.SetDefaultOutDir("")
	home := t.TempDir() + "/private-token/."
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv("SAMPLECLI_OUT_DIR", "")
	var stderr strings.Builder
	a := agentio.New("demo", agentio.WithStdout(&strings.Builder{}), agentio.WithStderr(&stderr), agentio.WithLimit(1))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "private-token") || len(runnableHints(stderr.String())) != 0 {
		t.Fatalf("cache fallback leaked the cleaned path: %q", stderr.String())
	}
}
