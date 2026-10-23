// Package agentio_test exercises the agent-first output contract from the
// outside, as an agent driving the CLI through Bash would observe it: stdout
// carries data only, stderr carries usage/errors/footer/hints, and exit codes
// follow README.md (0 ok, 1 runtime, 2 usage, 3 stale, 4 no data).
package agentio_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrothroc/agentio"
)

// demoRow is a synthetic output row; every fixture in this repository is invented.
type demoRow struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Quantity string `json:"quantity"`
}

func demoHeader() []string { return []string{"id", "name", "quantity"} }

func demoTSV(r demoRow) []string {
	return []string{strconv.Itoa(r.ID), r.Name, r.Quantity}
}

func demoObj(r demoRow) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "quantity": r.Quantity}
}

func demoRows(n int) []demoRow {
	rows := make([]demoRow, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, demoRow{ID: i, Name: "synthetic-" + strconv.Itoa(i), Quantity: "1.00"})
	}
	return rows
}

// newIO returns an AgentIO writing to buffers and a per-test overflow dir, so
// no test can ever touch the real default out directory.
func newIO(t *testing.T, opts ...agentio.Option) (*agentio.AgentIO, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	all := append([]agentio.Option{
		agentio.WithStdout(&out),
		agentio.WithStderr(&errOut),
		agentio.WithOverflowDir(t.TempDir()),
	}, opts...)
	return agentio.New("demo", all...), &out, &errOut
}

// TestUsageOnMissingArgsExit2: a command invoked with missing required args
// prints that command's usage to stderr and exits 2; stdout stays empty.
func TestUsageOnMissingArgsExit2(t *testing.T) {
	a, stdout, stderr := newIO(t)
	a.SetUsage(func(w io.Writer) {
		fmt.Fprintln(w, "Usage:")
		fmt.Fprintln(w, "  samplecli demo --item <id> [flags]")
	})

	err := agentio.NewUsageError("missing required argument <item>", "samplecli demo --item <id>")
	code := a.Fail(err)

	if code != agentio.ExitUsage {
		t.Fatalf("Fail(usage error) = %d, want %d", code, agentio.ExitUsage)
	}
	if got := stdout.String(); strings.TrimSpace(got) != "" {
		t.Errorf("usage text leaked to stdout: %q", got)
	}
	for _, want := range []string{
		"Usage:",
		"samplecli demo --item <id>",
		"[error] demo: missing required argument <item>",
		"Try: samplecli demo --item <id>",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}

	// A command or command group with missing args must not exit 0 or 1.
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestErrorFormatIncludesTry: errors are never a bare Go error; every error
// line is '[error] <command>: <what went wrong>. Try: <exact command>'.
func TestErrorFormatIncludesTry(t *testing.T) {
	t.Run("usage error carries the corrective command", func(t *testing.T) {
		var buf bytes.Buffer
		code := agentio.Fail("report", &buf, agentio.NewUsageError(
			"missing required argument <item>", "samplecli report --item <id>"))

		want := "[error] report: missing required argument <item>. Try: samplecli report --item <id>\n"
		if buf.String() != want {
			t.Fatalf("error line = %q, want %q", buf.String(), want)
		}
		if code != agentio.ExitUsage {
			t.Fatalf("code = %d, want %d", code, agentio.ExitUsage)
		}
	})

	t.Run("plain Go error maps to a runtime exit and is still labelled", func(t *testing.T) {
		var buf bytes.Buffer
		code := agentio.Fail("sync", &buf, fmt.Errorf("connection refused"))

		if !strings.HasPrefix(buf.String(), "[error] sync: connection refused") {
			t.Fatalf("error line = %q, want a labelled [error] line", buf.String())
		}
		if code != agentio.ExitRuntime {
			t.Fatalf("code = %d, want %d", code, agentio.ExitRuntime)
		}
	})

	t.Run("no-data and stale errors map to their documented exits", func(t *testing.T) {
		var buf bytes.Buffer
		if got := agentio.Fail("report", &buf, agentio.NewDataError("no records yet", "samplecli sync")); got != agentio.ExitNoData {
			t.Errorf("data error code = %d, want %d", got, agentio.ExitNoData)
		}
		if got := agentio.Fail("health", &buf, agentio.NewStaleError("last sync 90 days ago", "samplecli sync")); got != agentio.ExitStale {
			t.Errorf("stale error code = %d, want %d", got, agentio.ExitStale)
		}
	})
}

// TestStdoutHasNoFooter: the footer goes to stderr only; stdout is data only.
func TestStdoutHasNoFooter(t *testing.T) {
	a, stdout, stderr := newIO(t)
	if err := agentio.Tabled(a, demoHeader(), demoRows(2), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	if got := a.Footer(agentio.ExitOK); got != agentio.ExitOK {
		t.Fatalf("Footer returned %d, want %d", got, agentio.ExitOK)
	}

	stdoutLines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(stdoutLines) != 3 {
		t.Fatalf("stdout has %d lines, want header + 2 rows:\n%s", len(stdoutLines), stdout.String())
	}
	for _, line := range stdoutLines {
		if strings.Contains(line, "[samplecli") || strings.Contains(line, "[hint]") || strings.Contains(line, "[error]") {
			t.Errorf("decoration on stdout: %q", line)
		}
	}

	footer := stderr.String()
	if !strings.Contains(footer, "[samplecli demo | rows=2 | exit=0 | ") || !strings.Contains(footer, "ms]") {
		t.Errorf("stderr footer missing or malformed: %q", footer)
	}
}

// TestTruncationWritesOverflowFileAndHint: output is bounded by --limit; the
// complete result is written to the overflow file and stderr prints the path
// plus example grep/head commands.
func TestTruncationWritesOverflowFileAndHint(t *testing.T) {
	dir := t.TempDir()
	a, stdout, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(2))
	if err := agentio.Tabled(a, demoHeader(), demoRows(5), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}

	stdoutLines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(stdoutLines) != 3 { // header + 2 limited rows
		t.Fatalf("stdout has %d lines, want 3 (header + limit):\n%s", len(stdoutLines), stdout.String())
	}
	if strings.Contains(stdout.String(), "synthetic-3") {
		t.Errorf("row 3 leaked past --limit 2:\n%s", stdout.String())
	}

	hint := stderr.String()
	if !strings.Contains(hint, filepath.Join(dir, "samplecli-demo-")) {
		t.Errorf("hint does not name the overflow path under %s: %q", dir, hint)
	}
	if !strings.Contains(hint, "grep") || !strings.Contains(hint, "head") {
		t.Errorf("hint lacks grep/head examples: %q", hint)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "samplecli-demo-*.tsv"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no overflow file written (err=%v, matches=%v)", err, matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading overflow file: %v", err)
	}
	full := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(full) != 6 { // header + all 5 rows
		t.Fatalf("overflow file has %d lines, want 6:\n%s", len(full), string(body))
	}
	if !strings.Contains(string(body), "synthetic-5") {
		t.Errorf("overflow file is missing the truncated rows:\n%s", string(body))
	}
}

// TestOverflowCreatesDir0700: the overflow directory is created 0700 and
// overflow files are written 0600.
func TestOverflowCreatesDir0700(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "out")
	a, _, _ := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, demoHeader(), demoRows(3), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("overflow dir was not created: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("overflow dir mode = %o, want 700", got)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "samplecli-demo-*.tsv"))
	if len(matches) != 1 {
		t.Fatalf("want exactly one overflow file, got %v", matches)
	}
	fileInfo, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("stat overflow file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("overflow file mode = %o, want 600", got)
	}
}

// TestJSONLinesOutput: --json emits one JSON object per row on stdout and no
// TSV header.
func TestJSONLinesOutput(t *testing.T) {
	a, stdout, _ := newIO(t, agentio.WithJSON(true))
	if err := agentio.Tabled(a, demoHeader(), demoRows(3), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("JSON output has %d lines, want 3:\n%s", len(lines), stdout.String())
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", i, err, line)
		}
		if rec["id"] != float64(i+1) || rec["name"] != "synthetic-"+strconv.Itoa(i+1) {
			t.Errorf("line %d decoded to %v", i, rec)
		}
	}
}

// TestRedactsAccessURLUserinfo: no secret (a credential-bearing URL or its
// userinfo) may appear in stdout, stderr, error text, or overflow files.
func TestRedactsAccessURLUserinfo(t *testing.T) {
	const raw = "https://user-abc:s3cr3t-pass@api.example.invalid/data"
	agentio.RegisterAccessURL(raw)
	t.Cleanup(agentio.ResetSecrets)

	dir := t.TempDir()
	a, stdout, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))

	// The secret is in the visible row and in the overflow-only row, so both paths are tested.
	rows := []demoRow{{ID: 1, Name: raw, Quantity: "1.00"}, {ID: 2, Name: raw, Quantity: "2.00"}}
	if err := agentio.Tabled(a, demoHeader(), rows, demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	a.Fail(fmt.Errorf("cannot reach %s", raw))

	for name, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		for _, secret := range []string{"s3cr3t-pass", "user-abc:s3cr3t-pass"} {
			if strings.Contains(got, secret) {
				t.Errorf("%s leaked secret %q:\n%s", name, secret, got)
			}
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "samplecli-demo-*.tsv"))
	if len(matches) != 1 {
		t.Fatalf("want one overflow file, got %v", matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read overflow: %v", err)
	}
	if strings.Contains(string(body), "s3cr3t-pass") {
		t.Errorf("overflow file leaked the secret:\n%s", string(body))
	}
	// The redaction must be visible on stdout and in the file, not just an omission.
	if !strings.Contains(stdout.String(), "[REDACTED]") || !strings.Contains(string(body), "[REDACTED]") {
		t.Errorf("redaction marker missing: stdout %q file %q", stdout.String(), body)
	}
}

// TestFormatErrorFallbackNamesTheBinary: when a failure supplies no corrective
// command, the fallback hint is a real samplecli command, not a bare leaf name
// ("sync --help" would invoke the Unix /bin/sync).
func TestFormatErrorFallbackNamesTheBinary(t *testing.T) {
	got := agentio.FormatError("report", fmt.Errorf("boom"))
	if !strings.Contains(got, "Try: samplecli report --help") {
		t.Fatalf("fallback hint = %q, want it prefixed with the samplecli binary", got)
	}

	got = agentio.FormatError("samplecli report", fmt.Errorf("boom"))
	if !strings.Contains(got, "Try: samplecli report --help") {
		t.Fatalf("fallback hint = %q, want no duplicated binary prefix", got)
	}
}

// TestOverflowDoesNotFollowPlantedSymlink: the overflow file is created with
// O_EXCL, so a pre-existing path (for example a symlink an attacker planted) is
// never followed or overwritten; a new suffixed name is used instead.
func TestOverflowDoesNotFollowPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	a, _, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1), agentio.WithTimestamp("fixed"))
	// Plant a symlink exactly where the first overflow file would go.
	collision := filepath.Join(dir, "samplecli-demo-fixed.tsv")
	if err := os.Symlink(victim, collision); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := agentio.Tabled(a, demoHeader(), demoRows(3), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}

	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(body) != "do not touch" {
		t.Errorf("overflow wrote through the planted symlink: %q", string(body))
	}
	if !strings.Contains(stderr.String(), "samplecli-demo-fixed-1.tsv") {
		t.Errorf("collision did not fall back to a suffixed name: %q", stderr.String())
	}
}

// TestOverflowSlugUsesFullCommandPath: a nested command's overflow file is
// named with its whole path, so files from different commands do not collide.
func TestOverflowSlugUsesFullCommandPath(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	a := agentio.New("report list",
		agentio.WithStdout(&out), agentio.WithStderr(&errOut),
		agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, demoHeader(), demoRows(2), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "samplecli-report-list-*.tsv"))
	if len(matches) != 1 {
		t.Fatalf("want one samplecli-report-list overflow file, got %v", matches)
	}
}

// TestOverflowSlugStripsBinaryName: a full command path that already begins
// with the binary name does not produce a doubled 'samplecli-samplecli-' prefix.
func TestOverflowSlugStripsBinaryName(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	a := agentio.New("samplecli report list",
		agentio.WithStdout(&out), agentio.WithStderr(&errOut),
		agentio.WithOverflowDir(dir), agentio.WithLimit(1))
	if err := agentio.Tabled(a, demoHeader(), demoRows(2), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "samplecli-samplecli-*.tsv")); len(matches) != 0 {
		t.Errorf("doubled binary prefix in overflow name: %v", matches)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "samplecli-report-list-*.tsv")); len(matches) != 1 {
		t.Errorf("want one samplecli-report-list overflow file, got %v", matches)
	}
}

// TestReportedPreservesExitCode: an already-printed failure keeps its code
// through agentio.ExitCode, so the process boundary needs no extra wrapper.
func TestReportedPreservesExitCode(t *testing.T) {
	for _, code := range []int{agentio.ExitRuntime, agentio.ExitUsage, agentio.ExitStale, agentio.ExitNoData} {
		err := agentio.NewReported(code)
		if !agentio.Reported(err) {
			t.Fatalf("NewReported(%d) not recognized by Reported", code)
		}
		if got := agentio.ExitCode(err); got != code {
			t.Errorf("ExitCode(NewReported(%d)) = %d", code, got)
		}
	}
}

// TestRegisterSecretIgnoresShortValues: a value with fewer than three bytes
// after trimming surrounding whitespace is not registered, so it cannot
// over-redact ordinary output. (Longer values are registered whatever they
// contain.)
func TestRegisterSecretIgnoresShortValues(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)

	agentio.RegisterSecret(":")
	agentio.RegisterSecret("")
	agentio.RegisterSecret("ab")
	if got := agentio.Redact("a:b ab normal text"); got != "a:b ab normal text" {
		t.Fatalf("short value over-redacted output: %q", got)
	}

	agentio.RegisterSecret("a-long-secret")
	if got := agentio.Redact("token a-long-secret here"); !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("real secret was not redacted: %q", got)
	}
}

// TestResetStartsAFreshRun: Reset clears the row counter and the elapsed clock
// so a reused AgentIO reports the current run, not command-registration time.
func TestResetStartsAFreshRun(t *testing.T) {
	times := []time.Time{
		time.Unix(1000, 0), // New
		time.Unix(1001, 0), // Reset
		time.Unix(1004, 0), // Footer: 3000ms only if Reset restarted the clock
	}
	idx := 0
	clock := func() time.Time {
		v := times[idx]
		if idx < len(times)-1 {
			idx++
		}
		return v
	}

	var out, errOut bytes.Buffer
	a := agentio.New("demo",
		agentio.WithStdout(&out), agentio.WithStderr(&errOut),
		agentio.WithOverflowDir(t.TempDir()), agentio.WithClock(clock))
	if err := agentio.Tabled(a, demoHeader(), demoRows(2), demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	a.Reset()
	a.Footer(agentio.ExitOK)

	if !strings.Contains(errOut.String(), "rows=0") || !strings.Contains(errOut.String(), "3000ms") {
		t.Errorf("footer after Reset = %q, want rows=0 and the reset clock", errOut.String())
	}
}

// TestRedactsNestedStringMap: a credential inside a map[string]string value is
// redacted, not passed through by the default branch.
func TestRedactsNestedStringMap(t *testing.T) {
	agentio.RegisterSecret("super-secret-token")
	t.Cleanup(agentio.ResetSecrets)

	got, err := agentio.RedactValue(map[string]any{
		"headers": map[string]string{"Authorization": "Bearer super-secret-token"},
	})
	if err != nil {
		t.Fatalf("RedactValue: %v", err)
	}
	headers, ok := got.(map[string]any)["headers"].(map[string]string)
	if !ok || headers["Authorization"] != "Bearer [REDACTED]" {
		t.Errorf("RedactValue = %#v, want the structure kept and the token replaced", got)
	}
}

// TestFailIsIdempotentForReportedErrors: an already-printed failure is not
// printed a second time, but its exit code still comes through.
func TestFailIsIdempotentForReportedErrors(t *testing.T) {
	var buf bytes.Buffer
	code := agentio.Fail("demo", &buf, agentio.NewReported(agentio.ExitUsage))
	if code != agentio.ExitUsage {
		t.Fatalf("code = %d, want %d", code, agentio.ExitUsage)
	}
	if buf.Len() != 0 {
		t.Errorf("reported error was printed again: %q", buf.String())
	}
}

// TestRedactConcurrentRegisterAndRead exercises the copy-on-write registry
// under -race: registration may happen while rows are being redacted.
func TestRedactConcurrentRegisterAndRead(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			agentio.RegisterSecret(fmt.Sprintf("secret-value-%d", i))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = agentio.Redact("literal data cell with https://u:p@host/x")
	}
	<-done
	if strings.Contains(agentio.Redact("https://user:pw@host/x"), "pw@") {
		t.Errorf("URL userinfo not redacted")
	}
}

// TestRedactDoesNotRedactHostnamesAcrossQueryBoundaries: a URL with no
// userinfo must pass through untouched, even when an '@' appears later in a
// query or fragment; the userinfo matcher stops at RFC 3986 authority
// delimiters.
func TestRedactDoesNotRedactHostnamesAcrossQueryBoundaries(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)

	for _, plain := range []string{
		"https://api.example.com/accounts",
		"https://api.example.com?next=user@example.org",
		"https://api.example.com#frag@ment",
	} {
		if got := agentio.Redact(plain); got != plain {
			t.Errorf("Redact(%q) = %q, want it unchanged", plain, got)
		}
	}

	withCreds := "https://user:pw@api.example.com/accounts"
	if got := agentio.Redact(withCreds); strings.Contains(got, "user:pw") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Redact(%q) = %q, want the userinfo redacted", withCreds, got)
	}
}

// TestRegisterSecretConcurrentWritersDoNotLoseUpdates: registering from
// several goroutines at once must keep every distinct secret. Without writer
// serialisation the copy-on-write update drops all but the last registration.
func TestRegisterSecretConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agentio.RegisterSecret(fmt.Sprintf("concurrent-secret-%03d-END", i))
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		secret := fmt.Sprintf("concurrent-secret-%03d-END", i)
		if got := agentio.Redact("value " + secret + " end"); got != "value [REDACTED] end" {
			t.Errorf("secret %q was lost by a concurrent registration", secret)
		}
	}
}

// TestRedactKeepsLongestFirstOrdering: registering a short fragment after a
// longer secret still redacts the longer secret as a whole, not as a fragment.
func TestRedactKeepsLongestFirstOrdering(t *testing.T) {
	agentio.ResetSecrets()
	t.Cleanup(agentio.ResetSecrets)

	agentio.RegisterSecret("abc123")
	agentio.RegisterSecret("abc")
	if got := agentio.Redact("value abc123 end"); got != "value [REDACTED] end" {
		t.Errorf("Redact = %q, want the longer secret redacted as a whole", got)
	}
}

// TestRedactsRegisteredSecret:
// a registered secret that is not a URL is redacted from stdout, stderr and
// the overflow file, and the redaction is visible.
func TestRedactsRegisteredSecret(t *testing.T) {
	const raw = "synthetic-redaction-value"
	agentio.RegisterSecret(raw)
	t.Cleanup(agentio.ResetSecrets)

	dir := t.TempDir()
	a, stdout, stderr := newIO(t, agentio.WithOverflowDir(dir), agentio.WithLimit(1))

	// The secret is in the visible row and in the overflow-only row, so both paths are tested.
	rows := []demoRow{{ID: 1, Name: raw, Quantity: "1.00"}, {ID: 2, Name: raw, Quantity: "2.00"}}
	if err := agentio.Tabled(a, demoHeader(), rows, demoTSV, demoObj); err != nil {
		t.Fatalf("Tabled: %v", err)
	}
	a.Fail(fmt.Errorf("cannot reach %s", raw))

	for name, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(got, raw) {
			t.Errorf("%s leaked secret %q:\n%s", name, raw, got)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, agentio.Tool()+"-demo-*.tsv"))
	if len(matches) != 1 {
		t.Fatalf("want one overflow file, got %v", matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read overflow: %v", err)
	}
	if strings.Contains(string(body), raw) {
		t.Errorf("overflow file leaked the secret:\n%s", string(body))
	}
	// The redaction must be visible on stdout and in the file, not just an omission.
	if !strings.Contains(stdout.String(), "[REDACTED]") || !strings.Contains(string(body), "[REDACTED]") {
		t.Errorf("redaction marker missing: stdout %q file %q", stdout.String(), body)
	}
}
