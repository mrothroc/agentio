package agentio

import (
	"errors"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// Redacted is the marker substituted for any registered secret or URL
// userinfo. It is deliberately visible (rather than a silent omission) so an
// operator can tell that redaction happened.
const Redacted = "[REDACTED]"

// userinfoSpans finds the userinfo of every URL authority ("//user:pass@"),
// with or without a scheme, so a credential embedded in a URL is redacted even
// when it was never registered. A "//" starts an authority after "scheme:" or
// at the start of a token (not after a letter, digit or '/', as in a path's
// "files//name"). The authority runs to the first '/', '?' or '#', any
// Unicode space, or a character that is never valid in userinfo ('"', '<',
// '>', '`'). An apostrophe is valid userinfo, so it never ends the match: a
// URL wrapped in apostrophes may be over-redacted, never leaked. The userinfo
// is everything before the authority's last '@' (url.Parse accepts a raw '@'
// inside a password).
func userinfoSpans(s string) []span {
	var out []span
	for from := 0; ; {
		i := strings.Index(s[from:], "//")
		if i < 0 {
			return out
		}
		slashes := from + i
		start := slashes + 2
		from = start
		if slashes > 0 {
			if prev := s[slashes-1]; prev != ':' && (isSchemeByte(prev) || prev == '/') {
				continue // a "//" inside a path or word, not an authority
			}
		}
		lastAt := -1
		for end := start; end < len(s); {
			r, size := utf8.DecodeRuneInString(s[end:])
			if r == '/' || r == '?' || r == '#' || r == '"' || r == '<' || r == '>' || r == '`' ||
				unicode.IsSpace(r) {
				break
			}
			if r == '@' {
				lastAt = end
			}
			end += size
		}
		if lastAt > start {
			out = append(out, span{start, lastAt})
		}
	}
}

func isSchemeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.' || c == ':'
}

// secrets holds an immutable slice of registered secret values. It is
// replaced wholesale (copy-on-write) by RegisterSecret and ResetSecrets, so
// Redact loads it without locking.
var secrets atomic.Pointer[[]string]

// registerMu serialises writers, so two concurrent registrations cannot both
// copy the same list and lose one of the values.
var registerMu sync.Mutex

// minSecretLen is the shortest value registered for redaction. Shorter values
// (a single character, a bare ':') are ignored to limit over-redaction.
const minSecretLen = 3

// errCycle reports a value that refers to itself.
var errCycle = errors.New("agentio: value contains a reference cycle")

// RegisterSecret records a literal secret value that must never appear in
// output. Matching is exact and case-sensitive: no decoding or normalisation
// is applied. Each string is matched on its own, so a secret split across two
// calls, rows or fields (Text("abc") then Text("def")) is not detected. A
// value with fewer than three bytes once leading and trailing whitespace is
// trimmed is ignored, to limit over-redaction; the value is stored as given.
// Credential providers call this once they have read a credential.
func RegisterSecret(secret string) {
	if len(strings.TrimSpace(secret)) < minSecretLen {
		return
	}

	registerMu.Lock()
	defer registerMu.Unlock()

	current := loadSecrets()
	for _, existing := range current {
		if existing == secret {
			return
		}
	}
	next := append(append([]string(nil), current...), secret)
	secrets.Store(&next)
}

// loadSecrets returns the current immutable secret list.
func loadSecrets() []string {
	if list := secrets.Load(); list != nil {
		return *list
	}
	return nil
}

// RegisterAccessURL registers an access URL that embeds a credential, plus the
// pieces an error message might quote on their own: the userinfo, username and
// password, each both as written in the URL (percent-encoded) and decoded.
// Like RegisterSecret, pieces shorter than three bytes are not registered on
// their own; inside any URL they are still covered by the userinfo guard.
func RegisterAccessURL(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	RegisterSecret(raw)

	if spans := userinfoSpans(raw); len(spans) > 0 {
		info := raw[spans[0].start:spans[0].end]
		RegisterSecret(info)
		if user, pass, ok := strings.Cut(info, ":"); ok {
			RegisterSecret(user)
			RegisterSecret(pass)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return
	}
	RegisterSecret(u.User.Username())
	if password, ok := u.User.Password(); ok {
		RegisterSecret(password)
		RegisterSecret(u.User.Username() + ":" + password)
	}
}

type span struct{ start, end int }

// Redact returns s with every occurrence of a registered secret, and the
// userinfo of every URL ("//user:pass@"), replaced by Redacted. All matches
// are found in the original text first and then replaced together, so one
// replacement cannot hide another (for example a registered "https://user"
// cannot shield the password that follows it). Overlapping or adjacent matches
// become a single marker.
func Redact(s string) string {
	list := loadSecrets()
	hasURL := strings.Contains(s, "//")
	if len(list) == 0 && !hasURL {
		return s
	}
	var spans []span
	for _, secret := range list {
		for from := 0; ; {
			i := strings.Index(s[from:], secret)
			if i < 0 {
				break
			}
			spans = append(spans, span{from + i, from + i + len(secret)})
			from += i + 1
		}
	}
	if hasURL {
		spans = append(spans, userinfoSpans(s)...)
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	prev := 0
	for i := 0; i < len(spans); {
		start, end := spans[i].start, spans[i].end
		for i++; i < len(spans) && spans[i].start <= end; i++ {
			end = max(end, spans[i].end)
		}
		b.WriteString(s[prev:start])
		b.WriteString(Redacted)
		prev = end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// RedactValue returns a copy of v with Redact applied to every string value
// inside string, map[string]any, map[string]string, []any and []string,
// recursing through nested values of those types. Nil maps and slices stay
// nil (so they still encode as JSON null). Map keys, structs, pointers and
// other types (including values with custom JSON marshalling) are returned
// unchanged and are not scrubbed. A value that contains itself is an error.
func RedactValue(v any) (any, error) {
	return redactValue(v, map[ref]bool{})
}

// ref identifies a container on the current recursion path. A slice is its
// backing array plus its length, so two slices that merely share storage
// ("ok" and a shorter view of the same array) are not mistaken for a cycle.
type ref struct {
	ptr uintptr
	n   int
}

func redactValue(v any, path map[ref]bool) (any, error) {
	switch value := v.(type) {
	case string:
		return Redact(value), nil
	case map[string]any:
		if value == nil {
			return value, nil
		}
		p := ref{reflect.ValueOf(value).Pointer(), -1}
		if path[p] {
			return nil, errCycle
		}
		path[p] = true
		defer delete(path, p)
		out := make(map[string]any, len(value))
		for k, item := range value {
			r, err := redactValue(item, path)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case map[string]string:
		if value == nil {
			return value, nil
		}
		out := make(map[string]string, len(value))
		for k, item := range value {
			out[k] = Redact(item)
		}
		return out, nil
	case []any:
		if value == nil {
			return value, nil
		}
		if len(value) > 0 {
			p := ref{reflect.ValueOf(value).Pointer(), len(value)}
			if path[p] {
				return nil, errCycle
			}
			path[p] = true
			defer delete(path, p)
		}
		out := make([]any, len(value))
		for i, item := range value {
			r, err := redactValue(item, path)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []string:
		if value == nil {
			return value, nil
		}
		out := make([]string, len(value))
		for i, item := range value {
			out[i] = Redact(item)
		}
		return out, nil
	default:
		return v, nil
	}
}

// ResetSecrets clears the registered secret registry. It exists for tests so
// a registered credential cannot leak into another test's expectations.
func ResetSecrets() {
	registerMu.Lock()
	defer registerMu.Unlock()
	secrets.Store(nil)
}
