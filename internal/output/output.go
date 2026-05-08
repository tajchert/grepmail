// Package output renders matched messages in the format requested by the
// user. The Writer interface lets the search loop stream results without
// buffering the whole result set.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tajchert/grepmail/internal/index"
)

// Hit is the value passed to a Writer for each matched message. The Raw and
// Body fields may be nil; the writer must tolerate that.
type Hit struct {
	Entry index.Entry
	Raw   []byte // full message bytes (optional)
	Body  []byte // body only (optional)
}

// Writer streams Hits to an underlying io.Writer.
type Writer interface {
	Write(h Hit) error
	Close() error
}

// New returns a Writer for the given format ("summary", "json", "mbox",
// "raw", "count", "ids").
func New(format string, w io.Writer) (Writer, error) {
	switch format {
	case "", "summary":
		return &summaryWriter{w: w}, nil
	case "json":
		return &jsonWriter{w: w}, nil
	case "mbox":
		return &mboxWriter{w: w}, nil
	case "raw":
		return &rawWriter{w: w}, nil
	case "count":
		return &countWriter{w: w}, nil
	case "ids":
		return &idsWriter{w: w}, nil
	default:
		return nil, fmt.Errorf("unknown output format %q", format)
	}
}

type summaryWriter struct{ w io.Writer }

func (s *summaryWriter) Write(h Hit) error {
	date := ""
	if !h.Entry.Date.IsZero() {
		date = h.Entry.Date.Local().Format("2006-01-02 15:04")
	}
	from := truncate(cleanInline(h.Entry.From), 30)
	subj := truncate(cleanInline(h.Entry.Subject), 80)
	_, err := fmt.Fprintf(s.w, "%-6d  %-16s  %-30s  %s\n", h.Entry.Offset, date, from, subj)
	return err
}
func (s *summaryWriter) Close() error { return nil }

type jsonWriter struct {
	w     io.Writer
	first bool
	enc   *json.Encoder
}

func (j *jsonWriter) Write(h Hit) error {
	if j.enc == nil {
		j.enc = json.NewEncoder(j.w)
	}
	rec := map[string]any{
		"offset":     h.Entry.Offset,
		"length":     h.Entry.Length,
		"from":       h.Entry.From,
		"to":         h.Entry.To,
		"cc":         h.Entry.Cc,
		"subject":    h.Entry.Subject,
		"message_id": h.Entry.MessageID,
		"date":       formatDate(h.Entry.Date),
		"mime":       h.Entry.MIMEType,
	}
	return j.enc.Encode(rec)
}
func (j *jsonWriter) Close() error { return nil }

type mboxWriter struct{ w io.Writer }

func (m *mboxWriter) Write(h Hit) error {
	if h.Raw == nil {
		return fmt.Errorf("mbox output requires raw message bytes")
	}
	_, err := m.w.Write(h.Raw)
	if err != nil {
		return err
	}
	if len(h.Raw) > 0 && h.Raw[len(h.Raw)-1] != '\n' {
		_, err = m.w.Write([]byte("\n"))
	}
	return err
}
func (m *mboxWriter) Close() error { return nil }

type rawWriter struct{ w io.Writer }

func (r *rawWriter) Write(h Hit) error {
	if h.Raw == nil {
		return fmt.Errorf("raw output requires raw message bytes")
	}
	// Strip the leading "From " envelope so callers get RFC 5322 bytes.
	body := h.Raw
	if i := indexOfNewline(body); i >= 0 {
		body = body[i+1:]
	}
	_, err := r.w.Write(body)
	return err
}
func (r *rawWriter) Close() error { return nil }

func indexOfNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(_ Hit) error { c.n++; return nil }
func (c *countWriter) Close() error {
	_, err := fmt.Fprintln(c.w, c.n)
	return err
}

type idsWriter struct{ w io.Writer }

func (i *idsWriter) Write(h Hit) error {
	id := h.Entry.MessageID
	if id == "" {
		id = "-"
	}
	_, err := fmt.Fprintln(i.w, id)
	return err
}
func (i *idsWriter) Close() error { return nil }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func cleanInline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
