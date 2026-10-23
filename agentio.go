// Package agentio implements an agent-first output contract for command-line
// tools.
//
// The primary operator of each tool is an LLM agent driving the binary through
// a shell, so every command shares one I/O discipline:
//
//	stdout  data only: TSV with a header by default, or JSON Lines with --json
//	stderr  usage, '[error] <cmd>: <what>. Try: <command>' lines, the one-line
//	        footer, and overflow hints
//
// Exit codes are fixed: 0 ok, 1 runtime error, 2 usage error, 3 stale or
// unhealthy, 4 no data yet. Data output is bounded by a limit; the complete
// result is written to an overflow file whose path is announced on stderr.
//
// Configuration is process-wide and meant to be set once at start-up: each
// binary registers its name with SetTool (labels, help hints, the overflow
// file prefix and the <TOOL>_OUT_DIR override derive from it) and may register
// a home-relative default overflow directory with SetDefaultOutDir. An AgentIO
// captures the tool name when it is created.
//
// agentio depends only on the Go standard library. It writes files only when
// output overflows, starts no goroutines and makes no network calls.
package agentio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Exit codes of the output contract.
const (
	ExitOK      = 0
	ExitRuntime = 1
	ExitUsage   = 2
	ExitStale   = 3
	ExitNoData  = 4
)

// DefaultLimit is the number of data rows (or text lines) a command writes to
// stdout before truncating and spilling the full result to an overflow file.
const DefaultLimit = 200

// validName is the shape of a tool name: it is used in file names and in the
// <TOOL>_OUT_DIR environment variable, so it must be a plain token.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var (
	toolMu           sync.RWMutex
	toolName         string
	defaultOutDir    string
	defaultOutDirRaw string // as given, for output-time redaction decisions
)

// SetDefaultOutDir registers the tool's default overflow directory, relative
// to the user's home (for example "sample/out"). An empty value clears it. A
// path that is absolute or lexically leaves the home directory ("../x") is a
// programming error and panics. The check is lexical: a symlink inside the
// home directory is followed, so, like the directory's other ancestors, it is
// trusted.
func SetDefaultOutDir(rel string) {
	raw := rel
	rel = strings.TrimSpace(rel)
	if rel != "" && (filepath.IsAbs(rel) || !filepath.IsLocal(rel)) {
		panic(fmt.Sprintf("agentio: SetDefaultOutDir(%q) must be a relative path inside the home directory", Redact(raw)))
	}
	toolMu.Lock()
	defaultOutDir = rel
	defaultOutDirRaw = raw
	toolMu.Unlock()
}

// SetTool registers the binary name (for example "samplecli"). The name must
// be a plain token of letters, digits, '.', '_' and '-', starting with a letter
// or digit, with no surrounding whitespace; anything else is a programming
// error and panics.
func SetTool(name string) {
	// Validated as given (surrounding whitespace is rejected, not trimmed), so
	// the stored name is exactly what the caller registered.
	if !validName.MatchString(name) {
		panic(fmt.Sprintf("agentio: SetTool(%q) requires a plain tool name ([A-Za-z0-9][A-Za-z0-9._-]*)", Redact(name)))
	}
	toolMu.Lock()
	toolName = name
	toolMu.Unlock()
}

// Tool returns the registered binary name. Using the contract before a tool
// registered itself is a programming error and panics rather than printing a
// wrong or blank label.
func Tool() string {
	toolMu.RLock()
	defer toolMu.RUnlock()
	if toolName == "" {
		panic("agentio: SetTool was not called; each tool's root command must register its name")
	}
	return toolName
}

// AgentIO implements the output contract for one command invocation. Commands
// write data through Tabled or Text and finish with Footer; failures go
// through Fail, which prints the usage block for usage errors and a single
// '[error] ... Try: ...' line, and returns the exit code the process should
// use.
//
// The limit applies to the whole invocation: data rows from every Tabled call
// (and lines from Text) share it.
//
// An AgentIO is not safe for concurrent use. Build one per command run with New.
type AgentIO struct {
	tool        string
	command     string
	stdout      io.Writer
	stderr      io.Writer
	overflowDir string
	overflowRaw string // the directory as configured, before trimming or cleaning
	limit       int
	json        bool
	now         func() time.Time

	outErr  error // first stdout write failure; output ends for the invocation
	rows    int   // data rows produced, including those that went only to overflow files
	written int   // data rows (or text lines) written to stdout
	started time.Time
	usage   func(io.Writer)
	ts      string
	tsFixed bool
}

// Option customises an AgentIO at construction time.
type Option func(*AgentIO)

// WithStdout redirects data output. The default is os.Stdout.
func WithStdout(w io.Writer) Option {
	return func(a *AgentIO) {
		if w != nil {
			a.stdout = w
		}
	}
}

// WithStderr redirects diagnostics output. The default is os.Stderr.
func WithStderr(w io.Writer) Option {
	return func(a *AgentIO) {
		if w != nil {
			a.stderr = w
		}
	}
}

// WithLimit sets the maximum number of data rows written to stdout. A limit
// below 1 means DefaultLimit.
func WithLimit(limit int) Option {
	return func(a *AgentIO) { a.SetLimit(limit) }
}

// WithJSON selects JSON Lines output instead of TSV.
func WithJSON(enabled bool) Option {
	return func(a *AgentIO) { a.json = enabled }
}

// WithOverflowDir overrides the directory that overflow files are written to.
// Tests use t.TempDir() here (or <TOOL>_OUT_DIR) so a tool's real data
// directory is never touched.
func WithOverflowDir(dir string) Option {
	return func(a *AgentIO) {
		if dir != "" {
			a.overflowDir, a.overflowRaw = dir, dir
		}
	}
}

// WithClock overrides the clock used for timings and overflow file names. It
// exists so tests can make generated paths deterministic.
func WithClock(now func() time.Time) Option {
	return func(a *AgentIO) {
		if now != nil {
			a.now = now
		}
	}
}

// WithTimestamp overrides the timestamp embedded in overflow file names.
// Characters that are not safe in a file name are replaced.
func WithTimestamp(ts string) Option {
	return func(a *AgentIO) {
		if ts != "" {
			a.ts = ts
			a.tsFixed = true
		}
	}
}

// New builds an AgentIO for the named command (for example "report"). It
// captures the registered tool name, so a later SetTool does not change it.
func New(command string, opts ...Option) *AgentIO {
	tool := Tool()
	dir, raw := outDirFor(tool)
	// The command is kept as given: it is redacted at output time, before any
	// trimming or splitting, because secrets can be registered after New.
	if strings.TrimSpace(command) == "" {
		command = tool
	}
	a := &AgentIO{
		tool:        tool,
		command:     command,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		overflowDir: dir,
		overflowRaw: raw,
		limit:       DefaultLimit,
		now:         time.Now,
	}
	for _, opt := range opts {
		opt(a)
	}
	a.started = a.now()
	if a.ts == "" {
		a.ts = a.started.Format("20060102-150405")
	}
	return a
}

// OutDir resolves the overflow directory for the registered tool, using the
// first of:
//
//  1. $<TOOL>_OUT_DIR (the tool name upper-cased);
//  2. the SetDefaultOutDir directory under the user's home directory;
//  3. <user cache directory>/<tool>/out;
//  4. <temp dir>/<tool>-out-<uid>, when neither the home nor the cache
//     directory can be determined.
//
// Whatever is chosen, overflow writing refuses a directory that is a symlink,
// is not owned by the current user, or is writable by others.
func OutDir() string {
	dir, _ := outDirFor(Tool())
	return dir
}

// outDirFor returns the overflow directory and the raw text it came from
// (before trimming and joining), which output-time redaction must see.
func outDirFor(tool string) (dir, raw string) {
	env := os.Getenv(strings.ToUpper(tool) + "_OUT_DIR")
	if dir := strings.TrimSpace(env); dir != "" {
		return dir, env
	}
	toolMu.RLock()
	rel, relRaw := defaultOutDir, defaultOutDirRaw
	toolMu.RUnlock()
	if rel != "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, rel), home + string(os.PathSeparator) + relRaw
		}
	}
	sep := string(os.PathSeparator)
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		return filepath.Join(cache, tool, "out"), cache + sep + tool + sep + "out"
	}
	leaf := fmt.Sprintf("%s-out-%d", tool, os.Getuid())
	return filepath.Join(os.TempDir(), leaf), os.TempDir() + sep + leaf
}

// Command returns the command name used in the error prefix and footer, as
// given (not trimmed, not redacted). A blank name given to New becomes the
// tool name, and SetCommand ignores blank names.
func (a *AgentIO) Command() string { return a.command }

// Limit returns the effective limit.
func (a *AgentIO) Limit() int { return a.limit }

// JSON reports whether JSON Lines output is selected.
func (a *AgentIO) JSON() bool { return a.json }

// Stdout exposes the data writer for legitimate data-only output.
func (a *AgentIO) Stdout() io.Writer { return a.stdout }

// Stderr exposes the diagnostics writer.
func (a *AgentIO) Stderr() io.Writer { return a.stderr }

// SetUsage installs the callback that prints the invoked command's usage.
// Fail calls it for usage failures (exit 2), so a missing-argument error is
// self-correcting.
func (a *AgentIO) SetUsage(usage func(io.Writer)) { a.usage = usage }

// SetStdout overrides the data writer after construction.
func (a *AgentIO) SetStdout(w io.Writer) {
	if w != nil {
		a.stdout = w
	}
}

// SetStderr overrides the diagnostics writer after construction.
func (a *AgentIO) SetStderr(w io.Writer) {
	if w != nil {
		a.stderr = w
	}
}

// SetJSON selects JSON Lines output after construction.
func (a *AgentIO) SetJSON(enabled bool) { a.json = enabled }

// SetCommand overrides the command label used in error lines and the footer,
// for example with a nested command's full path ("report list").
func (a *AgentIO) SetCommand(name string) {
	if strings.TrimSpace(name) != "" {
		a.command = name
	}
}

// Reset starts a fresh command run: the elapsed-time clock, the row counters
// and any earlier stdout failure are cleared, so a reused AgentIO reports the
// current run.
func (a *AgentIO) Reset() {
	a.started = a.now()
	a.outErr = nil
	a.rows = 0
	a.written = 0
	if !a.tsFixed {
		a.ts = a.started.Format("20060102-150405")
	}
}

// SetLimit sets the limit. Values below 1 mean DefaultLimit.
func (a *AgentIO) SetLimit(limit int) {
	if limit > 0 {
		a.limit = limit
		return
	}
	a.limit = DefaultLimit
}

// Rows reports how many data rows this invocation delivered through Tabled,
// to stdout or to an overflow file. A row that failed to encode or write is
// not counted.
func (a *AgentIO) Rows() int { return a.rows }

// Tabled writes rows in the selected format. header and tsv must produce one
// field per column; obj must produce one JSON object per row. Every TSV field
// (header included) is redacted and escaped so embedded tabs and newlines
// cannot corrupt the table; JSON rows are redacted with RedactValue.
//
// Stdout receives data rows until the invocation's limit is used up. When
// rows are dropped, the complete table is written to an overflow file (TSV,
// or JSON Lines in JSON mode) and its path is announced on stderr.
func Tabled[T any](a *AgentIO, header []string, rows []T, tsv func(T) []string, obj func(T) map[string]any) error {
	if err := a.stdoutFailed(); err != nil {
		return err
	}
	shown := len(rows)
	if remaining := a.limit - a.written; shown > remaining {
		shown = max(remaining, 0)
	}

	if a.json {
		for i := 0; i < shown; i++ {
			line, err := jsonLine(obj(rows[i]))
			if err != nil {
				return err
			}
			if err := a.emit(line); err != nil {
				return err
			}
			a.written++
			a.rows++
		}
	} else {
		if err := a.emit(tsvLine(header) + "\n"); err != nil {
			return err
		}
		for i := 0; i < shown; i++ {
			if err := a.emit(tsvLine(tsv(rows[i])) + "\n"); err != nil {
				return err
			}
			a.written++
			a.rows++
		}
	}

	if shown < len(rows) {
		var path string
		var err error
		if a.json {
			path, err = createOverflow(a.overflowDir, a.overflowRaw, a.overflowBase(), ".jsonl", func(w io.Writer) error {
				return streamJSON(w, rows, obj)
			})
		} else {
			path, err = createOverflow(a.overflowDir, a.overflowRaw, a.overflowBase(), ".tsv", func(w io.Writer) error {
				return streamTable(w, header, rows, tsv)
			})
		}
		if err != nil {
			return err
		}
		a.rows += len(rows) - shown // delivered to the overflow file only
		a.overflowHints(fmt.Sprintf("%d rows exceed --limit %d; full result:", len(rows), a.limit), path, "head -n 20 -- %s")
	}
	return nil
}

// Text writes one redacted plain-text document (a message body, a report) to
// stdout, also in JSON mode (use Tabled for JSON output), bounded by the invocation's remaining limit, counted in lines. When
// the document is longer, stdout gets the lines that fit and the complete
// text goes to an overflow file announced on stderr. maxLines, when positive,
// lowers the bound for this document.
func (a *AgentIO) Text(body string, maxLines int) error {
	if err := a.stdoutFailed(); err != nil {
		return err
	}
	budget := a.limit - a.written
	if maxLines > 0 && maxLines < budget {
		budget = maxLines
	}
	budget = max(budget, 0)
	body = Redact(body)
	lines := strings.SplitAfter(body, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= budget {
		if err := a.emit(body); err != nil {
			return err
		}
		a.written += len(lines)
		return nil
	}
	if err := a.emit(strings.Join(lines[:budget], "")); err != nil {
		return err
	}
	a.written += budget
	path, err := createOverflow(a.overflowDir, a.overflowRaw, a.overflowBase(), ".txt", func(w io.Writer) error {
		_, err := io.WriteString(w, body)
		return err
	})
	if err != nil {
		return err
	}
	a.overflowHints(fmt.Sprintf("%d lines exceed %d; full text:", len(lines), budget), path,
		fmt.Sprintf("tail -n +%d -- %%s | head -n 50", budget+1))
	return nil
}

// emit writes s to stdout. The first failure (whatever the writer claims to
// have accepted) ends stdout output for the invocation, so a partly written
// row can never reopen the limit; Reset clears it.
func (a *AgentIO) emit(s string) error {
	if err := a.stdoutFailed(); err != nil {
		return err
	}
	n, err := io.WriteString(a.stdout, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	if err != nil {
		a.outErr = err
		return err
	}
	return nil
}

func (a *AgentIO) stdoutFailed() error {
	if a.outErr != nil {
		return fmt.Errorf("agentio: stdout failed earlier in this invocation: %w", a.outErr)
	}
	return nil
}

// jsonLine renders one redacted JSON Lines record, newline included.
func jsonLine(obj map[string]any) (string, error) {
	record, err := RedactValue(obj)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (a *AgentIO) hint(format string, args ...any) {
	fmt.Fprintf(a.stderr, "[hint] "+format+"\n", args...)
}

// overflowHints announces an overflow file. Runnable commands are suggested
// only when the path can be shown exactly: a path that must be escaped
// (control characters) or redacted (it contains a secret) is printed for
// reference only, because a command built from it would name another file.
// "more" is a command template with one %s for the quoted path.
func (a *AgentIO) overflowHints(summary, path, more string) {
	// Redaction is decided on the directory as configured, before trimming and
	// cleaning could break a secret apart, as well as on the actual path.
	asGiven := a.overflowRaw + string(os.PathSeparator) + filepath.Base(path)
	if diag(asGiven) != asGiven || diag(path) != path {
		a.hint("%s %s", summary, diag(asGiven))
		a.hint("the path above is escaped or redacted, so it is not runnable as printed; open the newest file in the overflow directory")
		return
	}
	quoted := shellQuote(path)
	a.hint("%s %s", summary, quoted)
	a.hint("inspect with: grep -e '<pattern>' -- %s | head -n 20", quoted)
	a.hint("or: "+more, quoted)
}

// tsvLine redacts and escapes fields so a secret never appears and a tab,
// newline, or carriage return inside a value cannot shift columns or split a row.
func tsvLine(fields []string) string {
	escaped := make([]string, len(fields))
	for i, field := range fields {
		escaped[i] = tsvField(Redact(field))
	}
	return strings.Join(escaped, "\t")
}

func tsvField(field string) string {
	field = strings.ReplaceAll(field, "\\", "\\\\")
	field = strings.ReplaceAll(field, "\t", "\\t")
	field = strings.ReplaceAll(field, "\n", "\\n")
	field = strings.ReplaceAll(field, "\r", "\\r")
	return field
}

// overflowMaxAttempts bounds the numeric-suffix retry when an overflow file
// name is already taken, so a persistent failure cannot loop forever.
const overflowMaxAttempts = 1000

// fileComponent makes s safe as one part of a file name: anything outside
// [A-Za-z0-9._-] becomes '_', and "", "." and ".." become "_".
func fileComponent(s string) string {
	out := []byte(s)
	for i, c := range out {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			out[i] = '_'
		}
	}
	clean := string(out)
	if clean == "" || clean == "." || clean == ".." {
		return "_"
	}
	return clean
}

// overflowBase is "<tool>-<command words>-<timestamp>", every part redacted
// first and then reduced to a plain file-name component (in that order, so
// sanitizing cannot break up a secret before redaction sees it). A leading tool-name word is dropped, so
// `samplecli report list` gives "samplecli-report-list-<ts>", not
// "samplecli-samplecli-report-list-<ts>".
func (a *AgentIO) overflowBase() string {
	fields := strings.Fields(Redact(a.command))
	if len(fields) > 1 && fields[0] == a.tool {
		fields = fields[1:]
	}
	slug := a.tool
	if len(fields) > 0 {
		slug = strings.Join(fields, "-")
	}
	return fmt.Sprintf("%s-%s-%s", fileComponent(Redact(a.tool)), fileComponent(slug), fileComponent(Redact(a.ts)))
}

// createOverflow writes one overflow file through write and returns its path.
//
// The directory is created 0700 when missing. It is refused when it is a
// symlink, is not a directory, is not owned by the current user, or is
// writable by group or others. The file is created inside a handle on that
// directory (os.Root), exclusively (O_CREATE|O_EXCL) and with mode 0600, so a
// planted file or symlink is never followed or overwritten, and a directory
// swapped in between the checks and the open is detected. A name collision
// retries with a numeric suffix; any other failure removes the partial file.
// Privacy rests on ownership and POSIX mode bits. Access control lists are
// not inspected (the standard library cannot read them), so ACLs on the
// directory, like its ancestors, are trusted: the tool or the user chooses
// them, not this package.
//
// Every filesystem error names the directory as configured (raw), never the
// cleaned path, so a registered spelling still matches when the error is
// redacted; the underlying OS error is reduced to its operation and reason.
// Serialization errors from write (which carry no path) are returned unchanged.
func createOverflow(dir, raw, base, ext string, write func(io.Writer) error) (string, error) {
	fail := func(reason string) error { return fmt.Errorf("agentio: overflow directory %s: %s", raw, reason) }
	// Clean first: with a trailing separator ("link/") Lstat would follow a
	// symlink and describe its target, bypassing the symlink check.
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fail(pathless(err))
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fail(pathless(err))
	}
	if reason := unsafeDir(info); reason != "" {
		return "", fail(reason)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fail(pathless(err))
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return "", fail(pathless(err))
	}
	if !os.SameFile(info, opened) {
		return "", fail("changed while it was being opened")
	}
	for attempt := 0; attempt < overflowMaxAttempts; attempt++ {
		name := base + ext
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d%s", base, attempt, ext)
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fail(pathless(err))
		}
		if err := write(file); err != nil {
			file.Close()
			_ = root.Remove(name)
			// A filesystem failure (disk full, quota) carries the cleaned path;
			// a serialization error carries none and is returned as is.
			var pe *fs.PathError
			if errors.As(err, &pe) {
				return "", fail(pathless(err))
			}
			return "", err
		}
		if err := file.Close(); err != nil {
			_ = root.Remove(name)
			return "", fail(pathless(err))
		}
		return filepath.Join(dir, name), nil
	}
	return "", fail(fmt.Sprintf("no free file name after %d attempts", overflowMaxAttempts))
}

// unsafeDir returns why info is not a safe overflow directory, or "".
func unsafeDir(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "is a symlink; refusing to write through it"
	case !info.IsDir():
		return "is not a directory"
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Sprintf("is writable by other users (mode %v); refusing", info.Mode().Perm())
	}
	return foreignOwner(info)
}

// pathless reduces an OS error to its operation and reason, dropping the
// (cleaned) path it carries.
func pathless(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return err.Error()
}

// streamTable writes the header and every row as redacted TSV.
func streamTable[T any](w io.Writer, header []string, rows []T, tsv func(T) []string) error {
	buf := bufio.NewWriter(w)
	if _, err := fmt.Fprintln(buf, tsvLine(header)); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(buf, tsvLine(tsv(row))); err != nil {
			return err
		}
	}
	return buf.Flush()
}

// streamJSON writes every row as a redacted JSON line.
func streamJSON[T any](w io.Writer, rows []T, obj func(T) map[string]any) error {
	buf := bufio.NewWriter(w)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		record, err := RedactValue(obj(row))
		if err != nil {
			return err
		}
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	return buf.Flush()
}

// shellQuote single-quotes s for a POSIX shell, so a suggested command can be
// pasted safely whatever the path contains.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// diag prepares text for a diagnostic line: secrets are redacted, then line
// breaks and other control characters are escaped, so a value can neither
// leak a credential nor forge an extra stderr line or terminal sequence.
func diag(s string) string { return escapeControls(Redact(s)) }

func escapeControls(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r):
			// Controls, Unicode line/paragraph separators and format characters
			// (including bidi overrides) could forge or disguise lines.
			if r <= 0xff {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Footer writes the one-line footer to stderr and returns code unchanged; the
// caller passes one of the Exit* codes (typically ExitOK, or Fail's result).
func (a *AgentIO) Footer(code int) int {
	a.FooterFor(a.command, code)
	return code
}

// FooterFor is Footer with an explicit command path, for subcommands such as
// "report list". An empty cmd (or the bare tool name) prints the root footer.
func (a *AgentIO) FooterFor(cmd string, code int) {
	elapsed := a.now().Sub(a.started).Milliseconds()
	cmd = strings.TrimSpace(Redact(cmd))
	name := a.tool
	switch {
	case cmd == "" || cmd == a.tool:
	case strings.HasPrefix(cmd, a.tool+" "):
		name = cmd
	default:
		name = a.tool + " " + cmd
	}
	fmt.Fprintf(a.stderr, "[%s | rows=%d | exit=%d | %dms]\n", escapeControls(Redact(name)), a.rows, code, elapsed)
}

// Fail writes the failure contract for err and returns the exit code the
// process should use. For usage failures it first prints the command's usage
// (the SetUsage callback's output, written verbatim: it is the tool's own
// text, so it is neither redacted nor reduced to one line);
// for every failure it then prints the single line
//
//	[error] <command>: <what went wrong>. Try: <exact corrective command>
//
// An already-reported error is not printed again.
func (a *AgentIO) Fail(err error) int {
	if err == nil {
		return ExitOK
	}
	code := ExitCode(err)
	if Reported(err) {
		return code
	}
	if code == ExitUsage && a.usage != nil {
		a.usage(a.stderr)
	}
	fmt.Fprintln(a.stderr, formatError(a.tool, a.command, err))
	return code
}

// Fail writes the failure line for err to w and returns its exit code. It is
// the form for callers that do not hold an AgentIO; it uses the registered tool.
func Fail(command string, w io.Writer, err error) int {
	if err == nil {
		return ExitOK
	}
	code := ExitCode(err)
	if Reported(err) {
		return code
	}
	fmt.Fprintln(w, FormatError(command, err))
	return code
}

// Failf is Fail for a formatted runtime error with a known corrective command.
//
// The format string and the arguments are redacted before formatting, so
// neither a verb such as %q nor %% processing can re-encode a secret out of
// reach. Strings, errors (as their Error text) and fmt.Stringer values (as
// their String text) are redacted; other values are formatted as given. The
// finished message is redacted again by Fail.
func (a *AgentIO) Failf(hint, format string, args ...any) int {
	return a.Fail(NewError(fmt.Sprintf(Redact(format), redactArgs(args)...), hint))
}

func redactArgs(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			out[i] = Redact(v)
		case error:
			out[i] = Redact(v.Error())
		case fmt.Stringer:
			out[i] = Redact(v.String())
		default:
			out[i] = arg
		}
	}
	return out
}

// coded is implemented by errors that carry an exit code.
type coded interface{ Code() int }

// ExitCode maps an error to the documented process exit code. A nil error is
// 0. An error carrying ExitRuntime, ExitUsage, ExitStale or ExitNoData keeps
// it; every other non-nil error, including one carrying 0 or an undocumented
// code, is ExitRuntime, so a failure can never report success.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var c coded
	if errors.As(err, &c) {
		switch code := c.Code(); code {
		case ExitRuntime, ExitUsage, ExitStale, ExitNoData:
			return code
		}
	}
	return ExitRuntime
}

// FormatError renders err as the contract's single error line (no trailing
// newline) for the registered tool. Every component, including the command
// label and the fallback hint, is redacted and has its control characters
// escaped.
func FormatError(command string, err error) string {
	return formatError(Tool(), command, err)
}

func formatError(tool, command string, err error) string {
	if err == nil {
		return ""
	}
	// Every part is redacted in its raw form first; trimming and punctuation
	// changes come after, so they cannot break up a secret.
	command = strings.TrimSpace(Redact(command))
	if command == "" {
		command = tool
	}
	what := Redact(err.Error())
	hint := ""
	var cr *CodedError
	if errors.As(err, &cr) {
		hint = Redact(cr.Hint())
		if cr.What() != "" {
			what = Redact(cr.What())
		}
	}
	what = strings.TrimRight(strings.TrimSpace(what), ".")
	if what == "" {
		what = "command failed"
	}
	if strings.TrimSpace(hint) == "" {
		// The contract always names a corrective command; fall back to the
		// command's own help, prefixed with the binary so it is runnable.
		hint = helpHint(tool, command)
	}
	// The tool name enters through the fallback label and hint, so the final
	// parts are redacted again.
	return fmt.Sprintf("[error] %s: %s. Try: %s", escapeControls(Redact(command)), escapeControls(what), escapeControls(Redact(strings.TrimSpace(hint))))
}

func helpHint(tool, command string) string {
	if command == tool || strings.HasPrefix(command, tool+" ") {
		return command + " --help"
	}
	return tool + " " + command + " --help"
}
