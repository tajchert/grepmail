package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

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
	// Only mmap the source when we actually need to read message bytes.
	// Header-only queries are answered entirely from the index, and the
	// mmap setup costs (tens of ms on multi-GB files on darwin) are pure
	// overhead in that case.
	var mf *mbox.MappedFile
	if needBody || needRaw {
		var err error
		mf, err = mbox.OpenMapped(opts.mboxPath)
		if err != nil {
			return err
		}
		defer mf.Close()
	}

	// Body matching dominates CPU on large mboxes — parallelize it.
	// Header-only queries stay serial (per-entry work is too small for
	// worker dispatch overhead to pay off). On Apple Silicon's 10–16
	// core M-series, body regex scales near-linearly so we use the
	// full GOMAXPROCS rather than capping.
	if needBody {
		workers := runtime.GOMAXPROCS(0)
		if workers >= 2 {
			return runIndexedParallel(opts, idx, w, mf, needRaw, workers)
		}
	}
	return runIndexedSerial(opts, idx, w, mf, needBody, needRaw)
}

// evalEntry runs the filter pipeline on one entry. Returns (hit, ok=true)
// if the entry passes both header and (when applicable) body filters.
func evalEntry(opts runOptions, mf *mbox.MappedFile, i int, e *index.Entry, needBody, needRaw bool) (output.Hit, bool) {
	view := entryToView(e, i)
	if !opts.spec.MatchHeaders(&view) {
		return output.Hit{}, false
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
	if opts.spec.NeedsBody() && !opts.spec.MatchBody(body, header, raw) {
		return output.Hit{}, false
	}
	hit := output.Hit{Entry: *e, Body: body}
	if needRaw {
		hit.Raw = raw
	}
	return hit, true
}

func runIndexedSerial(opts runOptions, idx *index.File, w output.Writer, mf *mbox.MappedFile, needBody, needRaw bool) error {
	hits := 0
	for i := range idx.Entries {
		hit, ok := evalEntry(opts, mf, i, &idx.Entries[i], needBody, needRaw)
		if !ok {
			continue
		}
		if err := w.Write(hit); err != nil {
			return err
		}
		hits++
		if opts.limit > 0 && hits >= opts.limit {
			break
		}
	}
	return nil
}

// runIndexedParallel fans entries out to a worker pool, preserving the
// original entry order on the output side via a queue of per-job result
// channels. Stop is signalled when the limit is reached or the writer
// errors; in flight workers drain naturally.
func runIndexedParallel(opts runOptions, idx *index.File, w output.Writer, mf *mbox.MappedFile, needRaw bool, workers int) error {
	type result struct {
		hit output.Hit
		ok  bool
	}
	type job struct {
		i  int
		e  *index.Entry
		rc chan result
	}

	jobs := make(chan job, workers*2)
	ordered := make(chan chan result, workers*4)
	stop := make(chan struct{})
	var stopOnce sync.Once
	signalStop := func() { stopOnce.Do(func() { close(stop) }) }

	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				hit, ok := evalEntry(opts, mf, j.i, j.e, true, needRaw)
				j.rc <- result{hit: hit, ok: ok}
			}
		}()
	}

	go func() {
		defer close(ordered)
		defer close(jobs)
		for i := range idx.Entries {
			select {
			case <-stop:
				return
			default:
			}
			rc := make(chan result, 1)
			select {
			case ordered <- rc:
			case <-stop:
				return
			}
			select {
			case jobs <- job{i: i, e: &idx.Entries[i], rc: rc}:
			case <-stop:
				return
			}
		}
	}()

	hits := 0
	var firstErr error
	for rc := range ordered {
		r := <-rc
		if !r.ok {
			continue
		}
		if firstErr != nil {
			continue
		}
		if err := w.Write(r.hit); err != nil {
			firstErr = err
			signalStop()
			continue
		}
		hits++
		if opts.limit > 0 && hits >= opts.limit {
			signalStop()
		}
	}
	wg.Wait()
	return firstErr
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
