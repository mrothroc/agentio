package agentio_test

import (
	"os"
	"testing"

	"github.com/mrothroc/agentio"
)

// TestMain redirects this package's overflow directory to a throwaway path.
// A tool's default directory holds real data, so even though every
// current test injects its own t.TempDir(), the harness guarantees no test can
// reach the real directory by omission.
func TestMain(m *testing.M) {
	agentio.SetTool("samplecli")
	dir, err := os.MkdirTemp("", "samplecli-agentio-overflow-")
	if err != nil {
		panic("agentio tests: create temp overflow dir: " + err.Error())
	}
	if err := os.Setenv("SAMPLECLI_OUT_DIR", dir); err != nil {
		panic("agentio tests: set SAMPLECLI_OUT_DIR: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
