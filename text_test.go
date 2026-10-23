package agentio_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

// TestTextWithinLimitIsWrittenWhole: a short document goes to stdout unchanged.
func TestTextWithinLimitIsWrittenWhole(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := agentio.New("show", agentio.WithStdout(&stdout), agentio.WithStderr(&stderr), agentio.WithOverflowDir(t.TempDir()))
	if err := a.Text("one\ntwo\n", 5); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "one\ntwo\n" || stderr.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

// TestTextBeyondLimitSpillsToOverflowFile: a long document is cut at maxLines on
// stdout; the whole text goes to a private overflow file announced on stderr.
func TestTextBeyondLimitSpillsToOverflowFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	a := agentio.New("show", agentio.WithStdout(&stdout), agentio.WithStderr(&stderr),
		agentio.WithOverflowDir(dir), agentio.WithTimestamp("20260101-000000"))
	var body strings.Builder
	for i := 0; i < 50; i++ {
		body.WriteString("row\n")
	}
	if err := a.Text(body.String(), 10); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(stdout.String(), "row"); got != 10 {
		t.Fatalf("stdout has %d rows, want 10", got)
	}
	hint := stderr.String()
	if !strings.Contains(hint, "50 lines exceed 10") {
		t.Fatalf("hint %q does not report the size", hint)
	}
	path := strings.Trim(strings.TrimSpace(strings.SplitN(strings.SplitN(hint, "full text: ", 2)[1], "\n", 2)[0]), "'")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body.String() {
		t.Fatal("overflow file does not hold the full text")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("overflow mode %v, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(hint, "grep -e '<pattern>' -- '"+path+"'") {
		t.Fatalf("hint %q lacks an inspect command", hint)
	}
}

// TestTextIsRedacted: a registered secret never reaches stdout or the overflow file.
func TestTextIsRedacted(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	agentio.RegisterSecret("hunter2-secret")
	var stdout bytes.Buffer
	dir := t.TempDir()
	a := agentio.New("show", agentio.WithStdout(&stdout), agentio.WithStderr(&bytes.Buffer{}), agentio.WithOverflowDir(dir))
	if err := a.Text("token hunter2-secret\nmore\nhunter2-secret\n", 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "hunter2") {
		t.Fatal("secret on stdout")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("overflow dir: %v %v", entries, err)
	}
	data, err := os.ReadFile(dir + "/" + entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") || strings.Count(string(data), "[REDACTED]") != 2 {
		t.Fatalf("overflow file = %q, want both secrets replaced", data)
	}
}
