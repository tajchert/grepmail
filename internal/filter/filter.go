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
	if s.BodyRe != nil || s.AnyRe != nil {
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
	if s.BodyRe != nil && !s.BodyRe.Match(body) {
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
	ct := bytes.ToLower(headerValue(header, []byte("content-type")))
	if !bytes.Contains(ct, []byte("multipart/")) {
		// A non-multipart message with Content-Disposition: attachment is rare
		// but possible.
		cd := bytes.ToLower(headerValue(header, []byte("content-disposition")))
		return bytes.Contains(cd, []byte("attachment"))
	}
	low := bytes.ToLower(body)
	return bytes.Contains(low, []byte("content-disposition: attachment")) ||
		bytes.Contains(low, []byte("filename="))
}

func attachmentNameMatches(body []byte, re *regexp.Regexp) bool {
	// Cheap scan for filename="..." or filename=... occurrences.
	low := body
	for {
		i := bytes.Index(bytes.ToLower(low), []byte("filename="))
		if i < 0 {
			return false
		}
		end := i + len("filename=")
		// Quoted form.
		rest := low[end:]
		var name []byte
		if len(rest) > 0 && rest[0] == '"' {
			rest = rest[1:]
			j := bytes.IndexByte(rest, '"')
			if j < 0 {
				return false
			}
			name = rest[:j]
		} else {
			j := bytes.IndexAny(rest, ";\r\n ")
			if j < 0 {
				name = rest
			} else {
				name = rest[:j]
			}
		}
		if re.Match(name) {
			return true
		}
		low = low[end:]
	}
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
