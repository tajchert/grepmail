// Package cli wires user-supplied flags into a filter.Spec and runs the
// search loop. All subcommands share the same filter flag surface so users
// can swap between, e.g., `grepmail search --from foo` and `grepmail count
// --from foo` without re-learning anything.
package cli

import (
	"bytes"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/tajchert/grepmail/internal/filter"
)

type filterFlags struct {
	body       string
	header     string
	any        string
	from       string
	to         string
	cc         string
	subject    string
	since      string
	until      string
	hasAttach  string
	attachName string
	mime       string
	msgid      string
	headerKV   stringList
	ignoreCase bool
	indexNum   int
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (ff *filterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&ff.body, "body", "", "regex applied to message body")
	fs.StringVar(&ff.header, "header-match", "", "regex applied to raw header block")
	fs.StringVar(&ff.any, "match", "", "regex applied to entire message (headers + body)")
	fs.StringVar(&ff.any, "e", "", "alias for --match")
	fs.StringVar(&ff.from, "from", "", "regex applied to From header")
	fs.StringVar(&ff.to, "to", "", "regex applied to To header")
	fs.StringVar(&ff.cc, "cc", "", "regex applied to Cc header")
	fs.StringVar(&ff.subject, "subject", "", "regex applied to Subject (RFC2047-decoded)")
	fs.StringVar(&ff.since, "since", "", "include messages on/after this date")
	fs.StringVar(&ff.until, "until", "", "include messages on/before this date")
	fs.StringVar(&ff.hasAttach, "has-attachment", "", "filter to (true|false)")
	fs.StringVar(&ff.attachName, "attachment-name", "", "regex applied to attachment filenames")
	fs.StringVar(&ff.mime, "mime", "", "regex applied to top-level Content-Type")
	fs.StringVar(&ff.msgid, "id", "", "exact Message-ID match (case-insensitive, no <>)")
	fs.Var(&ff.headerKV, "header", "header filter K=REGEX, repeatable")
	fs.BoolVar(&ff.ignoreCase, "i", false, "case-insensitive regex matching")
	fs.IntVar(&ff.indexNum, "n", -1, "match the message at this 0-based position")
}

func (ff *filterFlags) build() (*filter.Spec, error) {
	s := &filter.Spec{IgnoreCase: ff.ignoreCase, HeaderField: map[string]*regexp.Regexp{}}

	var err error
	for _, b := range []struct {
		dst **regexp.Regexp
		pat string
	}{
		{&s.BodyRe, ff.body},
		{&s.HeaderRe, ff.header},
		{&s.AnyRe, ff.any},
		{&s.From, ff.from},
		{&s.To, ff.to},
		{&s.Cc, ff.cc},
		{&s.Subject, ff.subject},
		{&s.AttachmentName, ff.attachName},
		{&s.MIMEType, ff.mime},
	} {
		if *b.dst, err = filter.CompileRegex(b.pat, ff.ignoreCase); err != nil {
			return nil, err
		}
	}

	// Literal --body fast-path: when the pattern has no regex
	// metacharacters, swap regex matching for bytes.Index / indexFold.
	// On large bodies this is several × faster than the regex engine,
	// and arm64's NEON-tuned bytes.IndexByte makes it faster still.
	if ff.body != "" && regexp.QuoteMeta(ff.body) == ff.body {
		lit := []byte(ff.body)
		if ff.ignoreCase {
			lit = bytes.ToLower(lit)
			s.BodyLiteralFold = true
		}
		s.BodyLiteral = lit
	}

	if ff.since != "" {
		t, err := parseDate(ff.since)
		if err != nil {
			return nil, fmt.Errorf("--since: %w", err)
		}
		s.Since = &t
	}
	if ff.until != "" {
		t, err := parseDate(ff.until)
		if err != nil {
			return nil, fmt.Errorf("--until: %w", err)
		}
		s.Until = &t
	}

	switch strings.ToLower(ff.hasAttach) {
	case "":
	case "true", "1", "yes", "y":
		v := true
		s.HasAttachment = &v
	case "false", "0", "no", "n":
		v := false
		s.HasAttachment = &v
	default:
		return nil, fmt.Errorf("--has-attachment: must be true or false")
	}

	for _, kv := range ff.headerKV {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--header expects KEY=REGEX, got %q", kv)
		}
		re, err := filter.CompileRegex(v, ff.ignoreCase)
		if err != nil {
			return nil, err
		}
		s.HeaderField[strings.TrimSpace(k)] = re
	}

	if ff.msgid != "" {
		s.MessageIDExact = strings.Trim(ff.msgid, "<>")
	}
	if ff.indexNum >= 0 {
		n := ff.indexNum
		s.IndexExact = &n
	}
	return s, nil
}
