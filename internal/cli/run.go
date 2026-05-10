package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tajchert/grepmail/internal/filter"
	"github.com/tajchert/grepmail/internal/index"
	"github.com/tajchert/grepmail/internal/mbox"
	"github.com/tajchert/grepmail/internal/output"
)

// runOptions controls a search run. format/limit/needRaw decide which body
// reads happen and which writer drains the hits.
type runOptions struct {
	mboxPath string
	spec     *filter.Spec
	format   string
	limit    int
	needRaw  bool
	useIndex bool // attempt to use the sidecar index
	out      io.Writer
}

// runSearch executes the spec end-to-end. It picks the fastest path: when
// an index exists and the query is header-only, it iterates the index and
// only reads the file when emitting raw output. Otherwise it streams.
func runSearch(opts runOptions) error {
	w, err := output.New(opts.format, opts.out)
	if err != nil {
		return err
	}
	defer w.Close()

	needBody := opts.spec.NeedsBody()
	needRaw := opts.needRaw || opts.format == "mbox" || opts.format == "raw"

	// Indexed path: only safe if the query is header-only or we're prepared
	// to seek for body reads.
	if opts.useIndex {
		idx, err := index.Load(opts.mboxPath)
		if err == nil {
			return runIndexed(opts, idx, w, needBody, needRaw)
		}
		if !errors.Is(err, index.ErrStale) && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "grepmail: ignoring index (%v)\n", err)
		}
	}

	return runStreaming(opts, w, needBody, needRaw)
}

func runStreaming(opts runOptions, w output.Writer, needBody, needRaw bool) error {
	f, sc, err := mbox.Open(opts.mboxPath)
	if err != nil {
		return err
	}
	defer f.Close()

	hits := 0
	for sc.Next() {
		m := sc.Message()
		view := messageToView(m)
		if !opts.spec.MatchHeaders(&view) {
			continue
		}

		var raw, body, header []byte
		if needBody || needRaw {
			raw, err = m.ReadRaw(f)
			if err != nil {
				return err
			}
			body, _ = m.ReadBody(f)
			header, _ = m.ReadHeaderBytes(f)
		}

		if opts.spec.NeedsBody() {
			if !opts.spec.MatchBody(body, header, raw) {
				continue
			}
		}

		entry := messageToEntry(m)
		if err := w.Write(output.Hit{Entry: entry, Raw: raw, Body: body}); err != nil {
			return err
		}
		hits++
		if opts.limit > 0 && hits >= opts.limit {
			break
		}
	}
	return sc.Err()
}

func runIndexed(opts runOptions, idx *index.File, w output.Writer, needBody, needRaw bool) error {
	mf, err := mbox.OpenMapped(opts.mboxPath)
	if err != nil {
		return err
	}
	defer mf.Close()

	hits := 0
	for i := range idx.Entries {
		e := &idx.Entries[i]
		view := entryToView(e, i)
		if !opts.spec.MatchHeaders(&view) {
			continue
		}

		var raw, body, header []byte
		if needBody || needRaw {
			raw = mf.Slice(e.Offset, e.Length)
			if e.HeaderEnd > 0 && e.HeaderEnd <= int64(len(raw)) {
				body = raw[e.HeaderEnd:]
				if nl := indexByte(raw, '\n'); nl >= 0 {
					header = raw[nl+1 : e.HeaderEnd]
				}
			}
		}
		if opts.spec.NeedsBody() {
			if !opts.spec.MatchBody(body, header, raw) {
				continue
			}
		}

		if err := w.Write(output.Hit{Entry: *e, Raw: raw, Body: body}); err != nil {
			return err
		}
		hits++
		if opts.limit > 0 && hits >= opts.limit {
			break
		}
	}
	return nil
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func messageToView(m *mbox.Message) filter.HeaderView {
	mt := strings.TrimSpace(strings.SplitN(m.Header.Get("Content-Type"), ";", 2)[0])
	return filter.HeaderView{
		From:      m.Header.Get("From"),
		To:        m.Header.Get("To"),
		Cc:        m.Header.Get("Cc"),
		Subject:   m.Subject(),
		MessageID: m.MessageID(),
		Date:      m.Date(),
		MIMEType:  mt,
		Header:    m.Header,
		Index:     m.Index,
	}
}

func entryToView(e *index.Entry, i int) filter.HeaderView {
	return filter.HeaderView{
		From:      e.From,
		To:        e.To,
		Cc:        e.Cc,
		Subject:   e.Subject,
		MessageID: e.MessageID,
		Date:      e.Date,
		MIMEType:  e.MIMEType,
		Header:    nil,
		Index:     i,
	}
}

func messageToEntry(m *mbox.Message) index.Entry {
	mt := strings.TrimSpace(strings.SplitN(m.Header.Get("Content-Type"), ";", 2)[0])
	return index.Entry{
		Offset:    m.Offset,
		Length:    m.Length,
		From:      m.Header.Get("From"),
		To:        m.Header.Get("To"),
		Cc:        m.Header.Get("Cc"),
		Subject:   m.Subject(),
		MessageID: m.MessageID(),
		Date:      m.Date(),
		MIMEType:  mt,
		HasMIME:   mt != "",
	}
}
