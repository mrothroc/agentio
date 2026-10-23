package agentio_test

// Regression tests for the fourth adversarial review round: every output path
// must redact the raw value before any normalization (splitting, trimming,
// joining, sanitizing) can break a secret apart.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

func TestNormalizationNeverRunsBeforeRedaction(t *testing.T) {
	cases := map[string]struct{ secret, command string }{
		"space in secret":    {"alpha beta", "show alpha beta"},
		"tab in secret":      {"alpha\tbeta", "show alpha\tbeta"},
		"double space":       {"alpha  beta", "show alpha  beta"},
		"leading space":      {" alpha-beta", "show alpha-beta"},
		"tool prefix inside": {"samplecli show", "samplecli show x"},
	}
	for name, c := range cases {
		agentio.ResetSecrets()
		agentio.RegisterSecret(c.secret)
		dir := t.TempDir()
		a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		a.SetCommand(c.command)
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
			t.Fatal(err)
		}
		a.Fail(errors.New("boom"))
		a.Footer(agentio.ExitRuntime)
		entries, _ := os.ReadDir(dir)
		for _, leak := range []string{"alpha", "beta"} {
			if name == "tool prefix inside" {
				leak = "samplecli-show"
			}
			if strings.Contains(entries[0].Name(), leak) || (name != "tool prefix inside" && strings.Contains(stderr.String(), leak)) {
				t.Errorf("%s: %q leaked: file %q stderr %q", name, leak, entries[0].Name(), stderr.String())
			}
		}
	}
	agentio.ResetSecrets()
}

func TestErrorPunctuationIsTrimmedOnlyAfterRedaction(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	for _, secret := range []string{"sensitive-token.", "sensitive-token...", " sensitive-token "} {
		agentio.ResetSecrets()
		agentio.RegisterSecret(secret)
		for _, err := range []error{
			errors.New("authentication failed: " + secret),
			agentio.NewError("authentication failed", "use "+secret),
		} {
			if line := agentio.FormatError("show", err); strings.Contains(line, "sensitive-token") {
				t.Errorf("secret %q reconstructed in %q", secret, line)
			}
		}
	}
}

func TestUserinfoStopsAtQuotesAndUnicodeSpace(t *testing.T) {
	agentio.ResetSecrets()
	cases := map[string]string{
		`https://user:pass@host";email="person@example.org`: `https://[REDACTED]@host";email="person@example.org`,
		"https://host person@example.org":                   "https://host person@example.org",
		"see 'https://u:p@h' and 'x@y'":                     "see 'https://[REDACTED]@h' and 'x@y'",
		"<https://u:p@h>@z":                                 "<https://[REDACTED]@h>@z",
		"https://user:pass@word@host/x":                     "https://[REDACTED]@host/x",
	}
	for in, want := range cases {
		if got := agentio.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// Rows already delivered stay counted when a later row fails.
func TestRowsCountDeliveredRowsWhenALaterRowFails(t *testing.T) {
	a, _, _ := newIO(t, agentio.WithJSON(true))
	bad := func(r row) map[string]any {
		if r.ID == "2" {
			return map[string]any{"id": make(chan int)}
		}
		return map[string]any{"id": r.ID}
	}
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, bad); err == nil {
		t.Fatal("encoding failure not reported")
	}
	if a.Rows() != 1 {
		t.Fatalf("Rows() = %d after one delivered JSON row, want 1", a.Rows())
	}
	b := agentio.New("demo", agentio.WithStdout(&failAfter{writes: 2}), agentio.WithStderr(&strings.Builder{}),
		agentio.WithOverflowDir(t.TempDir()))
	if err := agentio.Tabled(b, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil {
		t.Fatal("write failure not reported")
	}
	if b.Rows() != 1 {
		t.Fatalf("Rows() = %d after header + one TSV row, want 1", b.Rows())
	}
}

// shortWriter has only Write (no WriteString), and accepts one byte per call.
type shortWriter struct{ got []byte }

func (s *shortWriter) Write(p []byte) (int, error) {
	s.got = append(s.got, p[:1]...)
	return 1, nil
}

// A writer that reports a short write without an error is treated as failed.
func TestShortWriteIsAFailure(t *testing.T) {
	a := agentio.New("demo", agentio.WithStdout(&shortWriter{}), agentio.WithStderr(&strings.Builder{}),
		agentio.WithOverflowDir(t.TempDir()))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil {
		t.Fatal("short write not reported")
	}
	if err := a.Text("x\n", 0); err == nil {
		t.Fatal("output continued after a short write")
	}
}
