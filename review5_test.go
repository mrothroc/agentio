package agentio_test

// Regression tests for the fifth adversarial review round.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

func TestUserinfoScannerHandlesApostrophesAndAllSpaces(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	cases := map[string]string{
		// Apostrophes are valid userinfo characters.
		"https://user:pa'ssword@host/x": "https://[REDACTED]@host/x",
		"https://o'brien:pw@host":       "https://[REDACTED]@host",
		// A quoted URL ends at the space after it, so neighbouring text is untouched.
		"see 'https://u:p@h' and 'x@y'": "see 'https://[REDACTED]@h' and 'x@y'",
		// An apostrophe never ends userinfo (it is valid there), so the trailing "'@z" is over-redacted rather than risk a leak.
		"'https://u:p@h'@z": "'https://[REDACTED]@z",
		// Characters never valid in userinfo always end it.
		`https://user:pass@host";email="person@example.org`: `https://[REDACTED]@host";email="person@example.org`,
		"<https://u:p@h>@z": "<https://[REDACTED]@h>@z",
		// Every Unicode space ends it.
		"https://host\vperson@example.org":     "https://host\vperson@example.org",
		"https://host\u0085person@example.org": "https://host\u0085person@example.org",
		"https://host person@example.org":      "https://host person@example.org",
		// Last '@' of the authority.
		"https://user:pass@word@host/x": "https://[REDACTED]@host/x",
		"//user:password@host/path":     "//[REDACTED]@host/path",
	}
	for in, want := range cases {
		if got := agentio.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	agentio.RegisterAccessURL("https://user:p%61'ssword@host/x")
	if got := agentio.Redact("value p%61'ssword"); strings.Contains(got, "ssword") {
		t.Errorf("encoded password with an apostrophe not registered: %q", got)
	}
}

func TestToolNameIsRedactedInEveryLabel(t *testing.T) {
	withTool(t, "samplecli")
	withSecret(t, "samplecli")
	var stderr bytes.Buffer
	a := agentio.New("show", agentio.WithStderr(&stderr))
	a.Footer(agentio.ExitOK)
	a.Fail(errors.New("boom"))
	for _, line := range []string{stderr.String(), agentio.FormatError("show", errors.New("boom")), agentio.FormatError("", errors.New("boom"))} {
		if strings.Contains(line, "samplecli") {
			t.Errorf("tool name leaked: %q", line)
		}
	}
}

func TestOverflowPathIsRedactedBeforeNormalization(t *testing.T) {
	for name, c := range map[string]struct{ secret, rel string }{
		"dot segment":    {"private-token/.", "private-token/./out"},
		"trailing slash": {"private-token/", "private-token//out"},
	} {
		withSecret(t, c.secret)
		dir := t.TempDir() + "/" + c.rel // raw, not filepath.Join (which would clean it first)
		a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr.String(), "private-token") || len(runnableHints(stderr.String())) != 0 {
			t.Errorf("%s: path secret shown or runnable: %q", name, stderr.String())
		}
	}
}

func TestFailfRedactsArgumentsBeforeFormatting(t *testing.T) {
	withSecret(t, "alpha\tbeta")
	a, _, stderr := newIO(t)
	a.Failf("", "bad value %q", "alpha\tbeta")
	a.Failf("", "bad value %v", errors.New("alpha\tbeta"))
	if strings.Contains(stderr.String(), "alpha") {
		t.Fatalf("Failf leaked a formatted secret: %q", stderr.String())
	}
}

func TestConfigurationPanicsDoNotEchoSecrets(t *testing.T) {
	withTool(t, "samplecli")
	withSecret(t, "hunter2-secret")
	t.Cleanup(func() { agentio.SetDefaultOutDir("") })
	for name, f := range map[string]func(){
		"SetTool":          func() { agentio.SetTool("https://u:hunter2-secret@h") },
		"SetDefaultOutDir": func() { agentio.SetDefaultOutDir("/hunter2-secret") },
	} {
		func() {
			defer func() {
				if msg, _ := recover().(string); msg == "" || strings.Contains(msg, "hunter2") {
					t.Errorf("%s panic = %q", name, msg)
				}
			}()
			f()
		}()
	}
}

func TestRegisterSecretLengthRuleIsAfterTrimming(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	agentio.RegisterSecret(" ab ") // two bytes after trimming: ignored
	agentio.RegisterSecret("a b")  // three bytes after trimming: registered
	if got := agentio.Redact("x ab y"); got != "x ab y" {
		t.Errorf("short value registered: %q", got)
	}
	if got := agentio.Redact("x a b y"); got != "x [REDACTED] y" {
		t.Errorf("three-byte value not registered: %q", got)
	}
}

func TestCommandReturnsTheCommandAsGiven(t *testing.T) {
	if got := agentio.New(" show ").Command(); got != " show " {
		t.Fatalf("Command() = %q, want the raw value", got)
	}
}
