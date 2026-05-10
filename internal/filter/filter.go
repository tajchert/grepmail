// Package filter contains the predicate engine that decides whether a
// message matches a query. Filters compose with AND semantics — a message
// passes only if every configured predicate accepts it.
//
// The engine is split into two phases:
//
//  1. HeaderMatch runs against an already-parsed header set (or an Index
//     entry). It is body-free and so can be evaluated without reading the
//     message body — the index path uses this exclusively.
//  2. BodyMatch runs against the body bytes and is only invoked when the
//     query needs full-text search.
//
// This split is what lets indexed queries skip the body entirely when the
// user hasn't asked for body search.
package filter

import (
	"bytes"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Spec is a parsed filter configuration.
type Spec struct {
	BodyRe      *regexp.Regexp
	HeaderRe    *regexp.Regexp // matches against the full raw header block
	AnyRe       *regexp.Regexp // both body and headers
	From        *regexp.Regexp
	To          *regexp.Regexp
	Cc          *regexp.Regexp
	Subject     *regexp.Regexp
	HeaderField map[string]*regexp.Regexp // arbitrary --header K=PAT

	// BodyLiteral, when set, short-circuits BodyRe with a plain
	// substring search. It's populated when the user's --body pattern
	// has no regex metacharacters; bytes.Index / indexFold then beats
	// the regex engine handily on multi-MB bodies.
	BodyLiteral     []byte
	BodyLiteralFold bool

	Since *time.Time
	Until *time.Time

	HasAttachment  *bool // nil = don't care
	AttachmentName *regexp.Regexp
	MIMEType       *regexp.Regexp
	MessageIDExact string
	IndexExact     *int
	IgnoreCase     bool
}

// NeedsBody reports whether evaluating the spec requires reading the body.
func (s *Spec) NeedsBody() bool {
	if s.BodyRe != nil || s.BodyLiteral != nil || s.AnyRe != nil {
		return true
	}
	if s.HasAttachment != nil || s.AttachmentName != nil {
		return true
	}
	return false
}

// HeaderView is the minimum interface a candidate must expose for header
// filters. Both *mbox.Message and an index Entry can satisfy it via
// adapters in the cmd layer.
type HeaderView struct {
	From      string
	To        string
	Cc        string
	Subject   string
	MessageID string
	Date      time.Time
	MIMEType  string
	Header    mail.Header // optional; used for arbitrary --header filters
	Index     int
}

// MatchHeaders reports whether the header-only portion of the spec accepts v.
func (s *Spec) MatchHeaders(v *HeaderView) bool {
	if s.IndexExact != nil && *s.IndexExact != v.Index {
		return false
	}
	if s.MessageIDExact != "" && !strings.EqualFold(s.MessageIDExact, v.MessageID) {
		return false
	}
	if s.From != nil && !s.From.MatchString(v.From) {
		return false
	}
	if s.To != nil && !s.To.MatchString(v.To) {
		return false
	}
	if s.Cc != nil && !s.Cc.MatchString(v.Cc) {
		return false
	}
	if s.Subject != nil && !s.Subject.MatchString(v.Subject) {
		return false
	}
	if s.MIMEType != nil && !s.MIMEType.MatchString(v.MIMEType) {
		return false
	}
	if s.Since != nil && !v.Date.IsZero() && v.Date.Before(*s.Since) {
		return false
	}
	if s.Until != nil && !v.Date.IsZero() && v.Date.After(*s.Until) {
		return false
	}
	if s.Since != nil && v.Date.IsZero() {
		// Be conservative: if we asked for a date range and the message has no
		// parseable date, exclude it rather than match everything.
		return false
	}
	if v.Header != nil {
		for k, re := range s.HeaderField {
			if !re.MatchString(v.Header.Get(k)) {
				return false
			}
		}
	}
	return true
}

// MatchBody runs body/any predicates against the given body bytes. raw is
// the full message bytes (used for AnyRe). header is the raw header block.
func (s *Spec) MatchBody(body, header, raw []byte) bool {
	switch {
	case s.BodyLiteral != nil:
		if s.BodyLiteralFold {
			if indexFold(body, s.BodyLiteral) < 0 {
				return false
			}
		} else if !bytes.Contains(body, s.BodyLiteral) {
			return false
		}
	case s.BodyRe != nil && !s.BodyRe.Match(body):
		return false
	}
	if s.HeaderRe != nil && !s.HeaderRe.Match(header) {
		return false
	}
	if s.AnyRe != nil && !s.AnyRe.Match(raw) {
		return false
	}
	if s.HasAttachment != nil {
		got := looksLikeAttachment(header, body)
		if got != *s.HasAttachment {
			return false
		}
	}
	if s.AttachmentName != nil {
		if !attachmentNameMatches(body, s.AttachmentName) {
			return false
		}
	}
	return true
}

// looksLikeAttachment is a quick heuristic: multipart message containing a
// part with Content-Disposition: attachment or a filename= parameter.
func looksLikeAttachment(header, body []byte) bool {
	ct := headerValue(header, []byte("content-type"))
	if !containsFold(ct, []byte("multipart/")) {
		// A non-multipart message with Content-Disposition: attachment is rare
		// but possible.
		cd := headerValue(header, []byte("content-disposition"))
		return containsFold(cd, []byte("attachment"))
	}
	return containsFold(body, []byte("content-disposition: attachment")) ||
		containsFold(body, []byte("filename="))
}

func attachmentNameMatches(body []byte, re *regexp.Regexp) bool {
	// Cheap scan for filename="..." or filename=... occurrences. We search
	// case-insensitively without lowercasing the whole body each iteration.
	needle := []byte("filename=")
	rest := body
	for {
		i := indexFold(rest, needle)
		if i < 0 {
			return false
		}
		after := rest[i+len(needle):]
		var name []byte
		if len(after) > 0 && after[0] == '"' {
			after = after[1:]
			j := bytes.IndexByte(after, '"')
			if j < 0 {
				return false
			}
			name = after[:j]
		} else {
			j := bytes.IndexAny(after, ";\r\n ")
			if j < 0 {
				name = after
			} else {
				name = after[:j]
			}
		}
		if re.Match(name) {
			return true
		}
		rest = rest[i+len(needle):]
	}
}

// indexFold returns the index of the first ASCII-case-insensitive match of
// needle in haystack, or -1. needle must already be lowercase ASCII.
func indexFold(haystack, needleLower []byte) int {
	if len(needleLower) == 0 {
		return 0
	}
	first := needleLower[0]
	first2 := first
	if first >= 'a' && first <= 'z' {
		first2 = first - 32
	}
	for i := 0; i+len(needleLower) <= len(haystack); i++ {
		c := haystack[i]
		if c != first && c != first2 {
			continue
		}
		if equalFoldASCII(haystack[i:i+len(needleLower)], needleLower) {
			return i
		}
	}
	return -1
}

func containsFold(haystack, needleLower []byte) bool {
	return indexFold(haystack, needleLower) >= 0
}

// equalFoldASCII compares a and bLower (already lowercase ASCII) under
// ASCII case folding. Faster than bytes.EqualFold for ASCII-only needles.
func equalFoldASCII(a, bLower []byte) bool {
	if len(a) != len(bLower) {
		return false
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if c != bLower[i] {
			return false
		}
	}
	return true
}

func headerValue(header []byte, key []byte) []byte {
	// Linear scan; fine for one-off lookups.
	lines := bytes.Split(header, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if !bytes.EqualFold(bytes.TrimSpace(line[:colon]), key) {
			continue
		}
		return bytes.TrimSpace(line[colon+1:])
	}
	return nil
}

// CompileRegex compiles pat with case-insensitivity if ignoreCase is set.
func CompileRegex(pat string, ignoreCase bool) (*regexp.Regexp, error) {
	if pat == "" {
		return nil, nil
	}
	if ignoreCase && !strings.HasPrefix(pat, "(?i)") {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pat, err)
	}
	return re, nil
}
