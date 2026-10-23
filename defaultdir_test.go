package agentio_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mrothroc/agentio"
)

// TestOutDirUsesTheToolsRegisteredDefault: the process registers its default
// overflow directory (SetDefaultOutDir) rather than having it hard-coded, so
// each tool keeps overflow files beside its own private data.
func TestOutDirUsesTheToolsRegisteredDefault(t *testing.T) {
	withTool(t, "mailpipe")
	t.Setenv("MAILPIPE_OUT_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	agentio.SetDefaultOutDir("mail/out")
	t.Cleanup(func() { agentio.SetDefaultOutDir("") })
	if got, want := agentio.OutDir(), filepath.Join(home, "mail", "out"); got != want {
		t.Fatalf("OutDir() = %q, want %q", got, want)
	}
}

// TestOutDirEnvironmentOverrideWins: <TOOL>_OUT_DIR beats the registered default.
func TestOutDirEnvironmentOverrideWins(t *testing.T) {
	withTool(t, "mailpipe")
	agentio.SetDefaultOutDir("mail/out")
	t.Cleanup(func() { agentio.SetDefaultOutDir("") })
	t.Setenv("MAILPIPE_OUT_DIR", "/tmp/elsewhere")
	if got := agentio.OutDir(); got != "/tmp/elsewhere" {
		t.Fatalf("OutDir() = %q, want the environment override", got)
	}
}

// TestOutDirWithoutRegisteredDefaultUsesUserCache: a tool that registers no
// default never writes into another tool's data directory.
func TestOutDirWithoutRegisteredDefaultUsesUserCache(t *testing.T) {
	withTool(t, "mailpipe")
	t.Setenv("MAILPIPE_OUT_DIR", "")
	agentio.SetDefaultOutDir("")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := agentio.OutDir(), filepath.Join(cache, "mailpipe", "out"); got != want {
		t.Fatalf("OutDir() = %q, want %q", got, want)
	}
}
