package agentio

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A failed write (disk full, quota, file-size limit) returns an *fs.PathError
// carrying the cleaned path; it must be reported with the configured directory
// instead, while a serialization error without a path passes through intact.
func TestOverflowWriteErrorsNeverCarryTheCleanedPath(t *testing.T) {
	base := t.TempDir()
	raw := base + "/private-token/./out"
	dir := filepath.Clean(raw)
	diskFull := func(w io.Writer) error {
		return &fs.PathError{Op: "write", Path: filepath.Join(dir, "x.tsv"), Err: syscall.ENOSPC}
	}
	_, err := createOverflow(raw, raw, "x", ".tsv", diskFull)
	if err == nil || strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), raw) {
		t.Fatalf("write error = %v, want the configured directory and no cleaned path", err)
	}
	if !strings.Contains(err.Error(), "write: "+syscall.ENOSPC.Error()) {
		t.Fatalf("write error %q lost its operation and reason", err)
	}
	encoding := errors.New("agentio: value contains a reference cycle")
	if _, err := createOverflow(raw, raw, "y", ".tsv", func(io.Writer) error { return encoding }); err != encoding {
		t.Fatalf("serialization error = %v, want it unchanged", err)
	}
}
