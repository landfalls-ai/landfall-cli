package spool

import (
	"regexp"
	"strings"
)

// Redact removes obvious secrets from text before it is recorded.
//
// WHY AT ACCEPT TIME, NOT PUBLISH TIME (spec FR-010). Redaction has to happen
// while the machine still looks the way it did when the responder wrote the
// text — and, more importantly, BEFORE the secret is written to disk. A spool
// entry can sit through a crash, a restart, and a reboot before it publishes;
// redacting on the way out would mean the raw credential was durably stored in
// the meantime, in a file that outlives the process.
//
// DELIBERATELY CONSERVATIVE. This is a safety net for the obvious cases, not a
// DLP engine, and it is important not to oversell it:
//
//   - It cannot catch a secret that does not look like one. A bare token with
//     no prefix and no assignment is indistinguishable from an id.
//   - It errs toward redacting. A false positive costs a responder some
//     legibility in one shared line; a false negative puts a live credential in
//     an incident timeline that many people can read and that is retained.
//
// The real guarantee is that the agent should not hand over secrets at all.
// This exists because "should not" and "does not" differ under pressure.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range redactors {
		out = r.re.ReplaceAllString(out, r.with)
	}
	return out
}

type redactor struct {
	re   *regexp.Regexp
	with string
}

var redactors = []redactor{
	// Bearer tokens in an Authorization header or a curl line.
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._\-]{8,}`), "$1 [redacted]"},

	// Vendor-prefixed keys, which are unambiguous enough to match on sight.
	// Kept as a prefix + shape so a rotated key of the same family still hits.
	{regexp.MustCompile(`\b(sk-[A-Za-z0-9]{8,}|ghp_[A-Za-z0-9]{8,}|gho_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|xox[baprs]-[A-Za-z0-9\-]{8,}|AKIA[A-Z0-9]{12,})`), "[redacted]"},

	// key=value / key: value where the key names a secret. The value stops at
	// whitespace, quote, comma or semicolon so surrounding prose survives.
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|client[_-]?secret|auth)\b(\s*[:=]\s*)(["']?)([^\s"',;]{4,})`), "$1$2$3[redacted]"},

	// A URL with inline credentials — postgres://user:pw@host, https://u:p@h.
	{regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s:/@]+):([^\s@]+)@`), "$1$2:[redacted]@"},

	// PEM private key blocks: replace the body, keep the markers so the reader
	// can see that a key was there and has been removed.
	{regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?(-----END [A-Z ]*PRIVATE KEY-----)`), "$1[redacted]$2"},
}

// RedactAll applies Redact across a slice, returning a new slice.
func RedactAll(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = Redact(s)
	}
	return out
}

// looksRedacted reports whether Redact changed anything — used by the caller to
// tell the responder their text was altered, rather than silently rewriting
// what they wrote.
func looksRedacted(before, after string) bool {
	return before != after && strings.Contains(after, "[redacted]")
}
