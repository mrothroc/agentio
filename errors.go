package agentio

import (
	"errors"
	"fmt"
)

// CodedError is an error that carries the agent-first contract's three extra
// pieces of information: what happened, the exact command to try instead, and
// the process exit code the failure maps to. Commands return one of these
// (built with the NewError/NewUsageError/NewStaleError/NewDataError helpers)
// rather than a bare Go error, so the '[error] <cmd>: <what>. Try: <command>'
// line and the exit code always agree.
type CodedError struct {
	what string
	hint string
	code int
}

// Error implements error with the contract's message shape, so an error that
// escapes to a caller which only calls Error() still reads correctly. Every
// method tolerates a nil receiver (a typed nil stored in an error interface),
// which reads as a runtime failure with no details.
func (e *CodedError) Error() string {
	if e == nil {
		return "command failed"
	}
	if e.hint == "" {
		return e.what
	}
	return fmt.Sprintf("%s. Try: %s", e.what, e.hint)
}

// What returns the human-readable description of what went wrong.
func (e *CodedError) What() string {
	if e == nil {
		return ""
	}
	return e.what
}

// Hint returns the exact corrective command, or "" when none is known.
func (e *CodedError) Hint() string {
	if e == nil {
		return ""
	}
	return e.hint
}

// Code returns the documented process exit code for this failure.
func (e *CodedError) Code() int {
	if e == nil {
		return ExitRuntime
	}
	return e.code
}

// NewError builds a runtime failure (exit 1) with an explicit corrective
// command. hint may be empty when no corrective command applies.
func NewError(what, hint string) *CodedError {
	return &CodedError{what: what, hint: hint, code: ExitRuntime}
}

// NewUsageError builds a usage failure (exit 2): missing or invalid arguments.
// hint is the exact command the agent should run instead, and is required by
// the contract.
func NewUsageError(what, hint string) *CodedError {
	return &CodedError{what: what, hint: hint, code: ExitUsage}
}

// NewStaleError builds a stale/unhealthy failure (exit 3), used by health
// checks.
func NewStaleError(what, hint string) *CodedError {
	return &CodedError{what: what, hint: hint, code: ExitStale}
}

// NewDataError builds a no-data-yet failure (exit 4), used when a command has
// nothing to report because no data has been ingested.
func NewDataError(what, hint string) *CodedError {
	return &CodedError{what: what, hint: hint, code: ExitNoData}
}

// reportedErr carries an exit code for a failure that has already been written
// to stderr (by AgentIO.Fail). It exists so the entry point still receives the
// right exit code without printing the error a second time.
type reportedErr struct{ code int }

func (e *reportedErr) Error() string { return "error already reported" }

// Code returns the preserved exit code so agentio.ExitCode can recover it
// without requiring an extra wrapper at the call site.
func (e *reportedErr) Code() int {
	if e == nil {
		return ExitRuntime
	}
	return e.code
}

// NewReported wraps an already-printed failure, preserving its exit code for
// the process boundary without printing it again.
func NewReported(code int) error { return &reportedErr{code: code} }

// Reported reports whether err was already written to stderr, in which case
// the entry point must not print it again.
func Reported(err error) bool {
	var r *reportedErr
	return errors.As(err, &r)
}
