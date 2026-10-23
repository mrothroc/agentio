# agentio

An agent-first output contract for Go command-line tools that an LLM agent drives through a shell as often as a person does. Standard library only. Supported on macOS and Linux, with Go 1.27.3 or later.

## The contract

- **stdout carries data only:** TSV with a header row, or JSON Lines in JSON mode.
- **stderr carries everything else:**
  - usage text, which `Fail` prints for usage errors;
  - one error line per failure, in the form `[error] <cmd>: <what>. Try: <command>`;
  - overflow hints;
  - a footer, `[<tool> <cmd> | rows=N | exit=N | Xms]`.

  Every diagnostic agentio formats is a single line. Line breaks, control characters, Unicode line separators and bidi or format characters inside an error, hint, label or path are escaped, so they can't forge or disguise lines. The usage block is the tool's own text: it is written verbatim, without redaction or escaping.
- **Exit codes are fixed:**
  - `0` ok;
  - `1` runtime error;
  - `2` usage error;
  - `3` stale or unhealthy, for example missing credentials or rate limits, when the caller classifies them that way;
  - `4` no data yet.

  `ExitCode` maps any other code carried by a non-nil error, including 0, to `1`, so a failure can never report success. `Footer(code)` prints the code it is given, so callers pass one of these constants or `Fail`'s result.
- **Output is bounded.** One limit covers every `Tabled` and `Text` call in an invocation; the default is 200 rows or lines. Rows count as soon as they are written. When rows or lines don't fit:
  - The complete result goes to an overflow file: TSV, JSON Lines in JSON mode, or text.
  - Its path is announced on stderr.
  - When the path can be shown exactly, ready-to-paste `grep` and `head` (or `tail`) commands follow, with the path single-quoted for the shell. A path that has to be escaped or redacted is printed for reference only.
- **A failed write ends output.** After the first stdout write failure, every later `Tabled` and `Text` call in the invocation returns an error and writes nothing, until `Reset`.
- **`Text` is plain text.** It writes plain text even in JSON mode; use `Tabled` for JSON output.
- **Overflow files are private, as far as ownership and mode bits go.**
  - A missing overflow directory is created with mode 0700.
  - An existing one is refused if it is a symlink (a trailing `/` included), isn't owned by you, or is writable by group or others.
  - Files are created inside a handle on the directory (`os.Root`), exclusively and with mode 0600. They never replace an existing path, and a directory swapped in after the checks is detected.
  - File names are built only from `[A-Za-z0-9._-]`.
  - Access control lists aren't inspected, because the standard library can't read them. So ACLs on the overflow directory, like its parent folders, are trusted: the tool or the user chooses them.
- **Secrets are redacted.** Redaction removes registered secrets (`RegisterSecret`, `RegisterAccessURL`) and the userinfo of any URL (`//user:pass@`, with or without a scheme). It covers:
  - TSV headers and fields;
  - JSON rows;
  - text documents;
  - overflow files;
  - error lines, hints, labels and paths.

  Redaction always sees the raw value, before anything trims, splits, joins, cleans or escapes it. That includes overflow paths, whose errors and hints use the directory exactly as configured. `Failf` redacts its format string, plus its string, error and `Stringer` arguments, before formatting them. Configuration panics redact the rejected value. All matches are found before any replacement.

  A `//` starts a URL authority after `scheme:` or at the start of a word. It doesn't start one after a letter, digit or `/`, as in `files//name`. The userinfo runs to the authority's last `@` and stops at `/ ? #`, any Unicode space, and characters never valid in userinfo (`" < > \``). An apostrophe is valid userinfo, so it never ends the match: a URL wrapped in apostrophes may be over-redacted, but never leaked.

  Matching is literal and case-sensitive. A value with fewer than three bytes, once its surrounding whitespace is trimmed, isn't registered. Each string is checked on its own, so a secret split across two calls, rows or fields isn't detected. In JSON rows, redaction covers string values inside `map[string]any`, `map[string]string`, `[]any` and `[]string`, nested or not. Nil containers stay `null`. A value that contains itself is an error; views that merely share storage are fine. Map keys, structs and other types aren't scrubbed.

## Configuration

Configuration is process-wide and is set once, at start-up. Each `AgentIO` keeps the tool name it was created with.

```go
agentio.SetTool("my-tool")             // [A-Za-z0-9][A-Za-z0-9._-]*; MY_TOOL_OUT_DIR derives from it
agentio.SetDefaultOutDir("mytool/out") // optional; relative to $HOME, not absolute, must not lexically escape it
```

Overflow files go to the first of these that applies:
1. `$<TOOL>_OUT_DIR`, from the tool name upper-cased with other characters as `_`;
2. the `SetDefaultOutDir` directory under `$HOME`;
3. `<user cache dir>/<tool>/out`;
4. `<temp dir>/<tool>-out-<uid>`, when neither the home nor the cache directory is known.

The `SetDefaultOutDir` check is lexical: a symlink inside your home directory is followed and, like any parent folder, trusted.

## Wiring a command

agentio formats output and errors. The caller still parses flags, picks the error class, writes the footer, and exits with the returned code.

```go
func run(args []string) int {
	a := agentio.New("report list", agentio.WithJSON(jsonFlag), agentio.WithLimit(limitFlag))
	a.SetUsage(func(w io.Writer) { fmt.Fprintln(w, "usage: mytool report list [--json] [--limit N]") })
	if len(args) > 0 {
		return a.Footer(a.Fail(agentio.NewUsageError("unexpected argument "+args[0], "mytool report list --help")))
	}
	rows, err := load()
	if err != nil {
		return a.Footer(a.Fail(agentio.NewError(err.Error(), "mytool sync")))
	}
	if err := agentio.Tabled(a, []string{"id", "name"}, rows,
		func(r Row) []string { return []string{r.ID, r.Name} },
		func(r Row) map[string]any { return map[string]any{"id": r.ID, "name": r.Name} }); err != nil {
		return a.Footer(a.Fail(err))
	}
	return a.Footer(agentio.ExitOK) // every path ends with a footer; main calls os.Exit(run(os.Args[1:]))
}
```

`make check` is the gate: gofmt, vet, and tests with the race detector.

Releases are tagged commits, so the module proxy builds them from the git tree. Never publish a copy of a working directory: local, untracked files could end up in it.
