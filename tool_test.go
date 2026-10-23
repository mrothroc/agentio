package agentio_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

// withTool switches the process-wide tool for one test and restores it.
func withTool(t *testing.T, name string) {
	t.Helper()
	previous := agentio.Tool()
	agentio.SetTool(name)
	t.Cleanup(func() { agentio.SetTool(previous) })
}

// TestToolNameDrivesLabels: every label the contract prints names the tool
// the process registered, regardless of the binary name.
func TestToolNameDrivesLabels(t *testing.T) {
	for _, tool := range []string{"samplecli", "othercli"} {
		t.Run(tool, func(t *testing.T) {
			withTool(t, tool)
			var stderr bytes.Buffer
			a := agentio.New("report status", agentio.WithStderr(&stderr), agentio.WithStdout(&bytes.Buffer{}))
			a.Footer(agentio.ExitOK)
			if want := "[" + tool + " report status | rows=0 | exit=0 |"; !strings.HasPrefix(stderr.String(), want) {
				t.Fatalf("footer %q, want prefix %q", stderr.String(), want)
			}
			if got := agentio.New("").Command(); got != tool {
				t.Fatalf("default command %q, want %q", got, tool)
			}
			line := agentio.FormatError("report", errors.New("boom"))
			if want := "[error] report: boom. Try: " + tool + " report --help"; line != want {
				t.Fatalf("FormatError = %q, want %q", line, want)
			}
		})
	}
}

// TestToolOverflowEnvAndPrefix: the overflow directory honours <TOOL>_OUT_DIR
// and overflow files carry the tool's prefix.
func TestToolOverflowEnvAndPrefix(t *testing.T) {
	for _, tool := range []string{"samplecli", "othercli"} {
		t.Run(tool, func(t *testing.T) {
			withTool(t, tool)
			dir := t.TempDir()
			t.Setenv(strings.ToUpper(tool)+"_OUT_DIR", dir)
			var stdout, stderr bytes.Buffer
			a := agentio.New(tool+" report status", agentio.WithStdout(&stdout), agentio.WithStderr(&stderr), agentio.WithLimit(1))
			rows := [][]string{{"a"}, {"b"}}
			if err := agentio.Tabled(a, []string{"col"}, rows, func(r []string) []string { return r }, func(r []string) map[string]any { return map[string]any{"col": r[0]} }); err != nil {
				t.Fatal(err)
			}
			matches, err := filepath.Glob(filepath.Join(dir, tool+"-report-status-*"))
			if err != nil || len(matches) != 1 {
				entries, _ := os.ReadDir(dir)
				t.Fatalf("overflow files in %s: %v (err %v), want one %s-report-status-*", dir, entries, err, tool)
			}
		})
	}
}

// TestSetToolRejectsBlank: a blank tool name is a programming error.
func TestSetToolRejectsBlank(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("SetTool(\"  \") did not panic")
		}
	}()
	agentio.SetTool("  ")
}

func TestOutDirResolution(t *testing.T) {
	for _, tool := range []string{"samplecli", "othercli"} {
		t.Run(tool, func(t *testing.T) {
			withTool(t, tool)
			agentio.SetDefaultOutDir("sample/out")
			t.Cleanup(func() { agentio.SetDefaultOutDir("") })
			home := t.TempDir()
			t.Setenv("HOME", home)
			env := strings.ToUpper(tool) + "_OUT_DIR"
			for _, value := range []string{"", " \t"} {
				t.Setenv(env, value)
				if got := agentio.OutDir(); got != filepath.Join(home, "sample", "out") {
					t.Fatalf("default directory = %q", got)
				}
			}
			dir := t.TempDir()
			t.Setenv(env, "  "+dir+" \t")
			if got := agentio.OutDir(); got != dir {
				t.Fatalf("override directory = %q, want %q", got, dir)
			}
		})
	}
}
