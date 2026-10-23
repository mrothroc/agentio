package agentio_test

// Regression tests for the third adversarial review round.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

// Sanitizing a file name before redacting it let a credential through.
func TestOverflowFileNamesAreRedactedBeforeSanitizing(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	agentio.RegisterSecret("secret/value")
	for _, command := range []string{"fetch https://user:password@host/x", "show secret/value"} {
		dir := t.TempDir()
		a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		a.SetCommand(command)
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(dir)
		name := entries[0].Name()
		for _, leak := range []string{"password", "secret_value", "secret/value", "value"} {
			if strings.Contains(name, leak) || strings.Contains(stderr.String(), leak) {
				t.Errorf("%q: %q leaked via file name %q / hints %q", command, leak, name, stderr.String())
			}
		}
	}
}

// errWriter accepts accept bytes of the first write, then reports err; later
// writes fail outright.
type errWriter struct {
	accept int // -1: accept the whole first write
	used   bool
}

func (w *errWriter) Write(p []byte) (int, error) {
	if w.used {
		return 0, errors.New("closed")
	}
	w.used = true
	n := w.accept
	if n < 0 || n > len(p) {
		n = len(p)
	}
	return n, errors.New("write failed")
}

// Any stdout write failure ends output for the invocation, whatever the writer
// claims to have accepted, so the limit cannot be exceeded afterwards.
func TestStdoutFailureEndsOutputForTheInvocation(t *testing.T) {
	for _, accept := range []int{0, 1, -1} {
		for _, json := range []bool{false, true} {
			a := agentio.New("demo", agentio.WithStdout(&errWriter{accept: accept}), agentio.WithStderr(&strings.Builder{}),
				agentio.WithOverflowDir(t.TempDir()), agentio.WithLimit(1), agentio.WithJSON(json))
			if err := agentio.Tabled(a, []string{"id"}, twoRows()[:1], rowTSV, rowObj); err == nil {
				t.Fatalf("accept=%d json=%v: failure not reported", accept, json)
			}
			var out strings.Builder
			a.SetStdout(&out)
			if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil || out.Len() != 0 {
				t.Fatalf("accept=%d json=%v: output resumed after a failure: %q (%v)", accept, json, out.String(), err)
			}
			if err := a.Text("more\n", 0); err == nil || out.Len() != 0 {
				t.Fatalf("accept=%d json=%v: Text resumed after a failure", accept, json)
			}
		}
	}
}

// Userinfo may contain a raw '@' that url.Parse accepts; the whole of it is redacted.
func TestUserinfoIsRedactedUpToTheLastAt(t *testing.T) {
	agentio.ResetSecrets()
	if got := agentio.Redact("https://user:pass@word@host/x"); got != "https://[REDACTED]@host/x" {
		t.Fatalf("Redact = %q, want everything before the last '@' of the authority redacted", got)
	}
	for _, keep := range []string{"https://api.example.com?next=user@example.org", "https://api.example.com#frag@ment", "a //b c@d"} {
		if got := agentio.Redact(keep); got != keep {
			t.Errorf("Redact(%q) = %q, want unchanged", keep, got)
		}
	}
}
