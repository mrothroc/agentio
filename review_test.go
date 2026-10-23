package agentio_test

// Regression tests for an adversarial pre-publication review. Each test is an
// attack that succeeded against an earlier version.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrothroc/agentio"
)

const secret = "synthetic-secret"

func withSecret(t *testing.T, s string) {
	t.Helper()
	agentio.ResetSecrets()
	agentio.RegisterSecret(s)
	t.Cleanup(agentio.ResetSecrets)
}

func readOnly(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want exactly one overflow file in %s, got %v (%v)", dir, entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type row struct{ ID, Detail string }

func rowTSV(r row) []string       { return []string{r.ID} }
func rowObj(r row) map[string]any { return map[string]any{"id": r.ID, "detail": r.Detail} }

func twoRows() []row { return []row{{"1", "first"}, {"2", "second"}} }

// Finding 2: a secret used as a TSV column name reached stdout and the overflow file.
func TestHeadersAreRedacted(t *testing.T) {
	withSecret(t, secret)
	dir := t.TempDir()
	a, stdout, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, []string{secret}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	file := readOnly(t, dir)
	if !strings.HasPrefix(stdout.String(), "[REDACTED]\n") || !strings.HasPrefix(file, "[REDACTED]\n") {
		t.Fatalf("header not redacted in place: stdout %q file %q", stdout.String(), file)
	}
}

// Finding 3: overflow paths, command labels and footer labels skipped redaction.
func TestDiagnosticsAreRedacted(t *testing.T) {
	withSecret(t, secret)
	dir := filepath.Join(t.TempDir(), "out-"+secret)
	a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	if err := a.Text("a\nb\nc\n", 1); err != nil {
		t.Fatal(err)
	}
	a.Footer(agentio.ExitOK)
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("overflow hint leaked the path secret:\n%s", stderr.String())
	}
	for _, line := range []string{
		agentio.FormatError(secret, errors.New("boom")),
		agentio.FormatError("fetch https://user:password@host/x", errors.New("boom")),
	} {
		if strings.Contains(line, secret) || strings.Contains(line, "password") {
			t.Fatalf("FormatError leaked through the label or fallback hint: %q", line)
		}
	}
	var footer bytes.Buffer
	agentio.New(secret, agentio.WithStderr(&footer)).Footer(agentio.ExitOK)
	if strings.Contains(footer.String(), secret) {
		t.Fatalf("footer leaked the label: %q", footer.String())
	}
}

// Finding 4: scheme-relative userinfo was missed, and a registered secret that
// overlapped a URL destroyed the delimiter the URL guard needed.
func TestURLUserinfoIsRedactedInEveryForm(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	for _, in := range []string{
		"//user:password@host/path",
		"see https://user:password@host/path",
		"HTTPS://user:password@host",
		"ftp://user:password@host",
	} {
		if got := agentio.Redact(in); strings.Contains(got, "password") || !strings.Contains(got, "[REDACTED]") {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
	agentio.RegisterSecret("https://user")
	if got := agentio.Redact("https://user:password@host/path"); strings.Contains(got, "password") {
		t.Fatalf("registered prefix let the password through: %q", got)
	}
}

// Finding 14: the percent-encoded form of a URL password stayed visible.
func TestAccessURLEncodedFragmentsAreRedacted(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)
	agentio.RegisterAccessURL("https://alice:p%40ssword@host/x")
	for _, in := range []string{"p%40ssword", "p@ssword", "alice:p%40ssword"} {
		if got := agentio.Redact("value " + in); strings.Contains(got, "ssword") {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
}

// Finding 5: command, tool and timestamp components could traverse out of the overflow directory.
func TestOverflowFileNamesCannotEscapeTheDirectory(t *testing.T) {
	dir := t.TempDir()
	a := agentio.New("x/../../escaped", agentio.WithStdout(&bytes.Buffer{}), agentio.WithStderr(&bytes.Buffer{}),
		agentio.WithOverflowDir(dir), agentio.WithLimit(1), agentio.WithTimestamp("../../ts"))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || strings.ContainsAny(entries[0].Name(), `/\`) {
		t.Fatalf("overflow file not a plain name inside the directory: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dir)), "escaped")); err == nil {
		t.Fatal("file written outside the overflow directory")
	}
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", name)
		}
	}()
	f()
}

// Finding 5: tool names and default directories are validated where they are configured.
func TestConfigurationRejectsUnsafeValues(t *testing.T) {
	withTool(t, "samplecli")
	t.Cleanup(func() { agentio.SetDefaultOutDir("") })
	mustPanic(t, "SetTool with a path", func() { agentio.SetTool("../evil") })
	mustPanic(t, "SetTool with a space", func() { agentio.SetTool("two words") })
	mustPanic(t, "SetDefaultOutDir escaping home", func() { agentio.SetDefaultOutDir("../outside") })
	mustPanic(t, "SetDefaultOutDir absolute", func() { agentio.SetDefaultOutDir("/etc/out") })
	agentio.SetDefaultOutDir("sample/out")
}

// Finding 6: a symlinked or shared-writable overflow directory was used silently.
func TestOverflowRefusesUnsafeDirectories(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"symlink": link, "world-writable": shared} {
		a, _, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err == nil {
			t.Errorf("%s directory accepted", name)
		}
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v", entries)
	}
}

// Finding 7: suggested shell commands interpolated paths unquoted.
func TestHintsQuotePathsForTheShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "o;id;# x")
	a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if strings.Contains(line, "o;id;#") && !strings.Contains(line, "'"+dir) {
			t.Fatalf("unquoted path in hint: %q", line)
		}
	}
	if !strings.Contains(stderr.String(), "grep -e '<pattern>' -- '") {
		t.Fatalf("grep hint not hardened: %s", stderr.String())
	}
}

// Finding 8: an error, hint or label containing a newline forged extra stderr lines.
func TestDiagnosticsStayOnOneLine(t *testing.T) {
	cases := []string{
		agentio.FormatError("show", errors.New("first\n[error] forged")),
		agentio.FormatError("show", agentio.NewError("boom", "run\nthis")),
		agentio.FormatError("sh\x1bow", errors.New("boom")),
	}
	for _, line := range cases {
		if strings.ContainsAny(line, "\n\r\x1b") {
			t.Errorf("diagnostic not single-line and control-free: %q", line)
		}
	}
	var footer bytes.Buffer
	agentio.New("a\nb", agentio.WithStderr(&footer)).Footer(agentio.ExitOK)
	if strings.Count(footer.String(), "\n") != 1 {
		t.Fatalf("footer spans lines: %q", footer.String())
	}
}

type foreignCoded struct{ code int }

func (f foreignCoded) Error() string { return "foreign" }
func (f foreignCoded) Code() int     { return f.code }

// Finding 9: a non-nil error could map to exit 0 or an undocumented code.
func TestExitCodesAreAlwaysDocumentedFailures(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"zero CodedError": {&agentio.CodedError{}, agentio.ExitRuntime},
		"reported 99":     {agentio.NewReported(99), agentio.ExitRuntime},
		"reported 0":      {agentio.NewReported(0), agentio.ExitRuntime},
		"foreign 0":       {foreignCoded{0}, agentio.ExitRuntime},
		"foreign 42":      {foreignCoded{42}, agentio.ExitRuntime},
		"reported usage":  {agentio.NewReported(agentio.ExitUsage), agentio.ExitUsage},
		"stale":           {agentio.NewStaleError("x", ""), agentio.ExitStale},
		"no data":         {agentio.NewDataError("x", ""), agentio.ExitNoData},
	}
	for name, c := range cases {
		if got := agentio.ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", name, got, c.want)
		}
	}
	if agentio.ExitCode(nil) != agentio.ExitOK {
		t.Fatal("nil must be success")
	}
}

// Finding 10: changing the tool after New mixed old and new identities.
func TestInstanceKeepsTheToolItWasCreatedWith(t *testing.T) {
	withTool(t, "alpha")
	var stderr bytes.Buffer
	dir := t.TempDir()
	a := agentio.New("cmd", agentio.WithStderr(&stderr), agentio.WithStdout(&bytes.Buffer{}),
		agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	agentio.SetTool("beta")
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	a.Footer(agentio.ExitOK)
	if !strings.Contains(stderr.String(), "[alpha cmd |") {
		t.Fatalf("footer = %q, want the alpha identity", stderr.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "alpha-") {
		t.Fatalf("overflow = %v, want the alpha prefix", entries)
	}
}

// Finding 11: the limit and row count restarted on every Tabled call.
func TestLimitAndRowsSpanTheWholeInvocation(t *testing.T) {
	dir := t.TempDir()
	a, stdout, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	for i := 0; i < 2; i++ {
		if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
			t.Fatal(err)
		}
	}
	data := 0
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line != "id" {
			data++
		}
	}
	if data != 1 || a.Rows() != 4 {
		t.Fatalf("stdout data rows %d (want 1), Rows() %d (want 4):\n%s", data, a.Rows(), stdout.String())
	}
}

// Finding 12: in JSON mode the overflow file was TSV and lost JSON-only fields.
func TestJSONOverflowKeepsTheJSONRepresentation(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1), agentio.WithJSON(true))
	if err := agentio.Tabled(a, []string{"id"}, twoRows(), rowTSV, rowObj); err != nil {
		t.Fatal(err)
	}
	body := readOnly(t, dir)
	lines := strings.Split(strings.TrimSpace(body), "\n")
	var last map[string]any
	if len(lines) != 2 || json.Unmarshal([]byte(lines[1]), &last) != nil || last["detail"] != "second" {
		t.Fatalf("overflow = %q, want two JSON lines with detail", body)
	}
	entries, _ := os.ReadDir(dir)
	if !strings.HasSuffix(entries[0].Name(), ".jsonl") {
		t.Fatalf("overflow name %q, want .jsonl", entries[0].Name())
	}
}

// Finding 13: redaction turned null into [] or {}, and a cyclic value recursed forever.
func TestRedactValuePreservesNilAndRejectsCycles(t *testing.T) {
	got, err := agentio.RedactValue(map[string]any{"s": []string(nil), "m": map[string]any(nil), "a": []any(nil), "ms": map[string]string(nil)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if string(encoded) != `{"a":null,"m":null,"ms":null,"s":null}` {
		t.Fatalf("nil containers changed: %s", encoded)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := agentio.RedactValue(cyclic); err == nil {
		t.Fatal("cycle not rejected")
	}
	a, _, _ := newIO(t, agentio.WithOverflowDir(t.TempDir()), agentio.WithJSON(true))
	if err := agentio.Tabled(a, []string{"id"}, []row{{"1", ""}}, rowTSV, func(row) map[string]any { return cyclic }); err == nil {
		t.Fatal("Tabled accepted a cyclic row")
	}
}

// Finding 15: with no home or cache directory the fallback was a predictable shared path.
func TestOutDirFallbackIsPerUserWhenHomeIsUnknown(t *testing.T) {
	withTool(t, "samplecli")
	t.Setenv("SAMPLECLI_OUT_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	want := filepath.Join(os.TempDir(), fmt.Sprintf("samplecli-out-%d", os.Getuid()))
	if got := agentio.OutDir(); got != want {
		t.Fatalf("OutDir() = %q, want %q", got, want)
	}
}

// Finding 20: WithLimit below 1 kept an earlier value instead of the default.
func TestWithLimitBelowOneUsesTheDefault(t *testing.T) {
	a := agentio.New("cmd", agentio.WithLimit(1), agentio.WithLimit(0))
	if a.Limit() != agentio.DefaultLimit {
		t.Fatalf("Limit() = %d, want DefaultLimit", a.Limit())
	}
}
