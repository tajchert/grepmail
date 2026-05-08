package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/tajchert/grepmail/internal/index"
	"github.com/tajchert/grepmail/internal/mbox"
)

const usage = `grepmail — fast CLI search for mbox files

Usage:
  grepmail <command> [flags] <mbox>

Commands:
  search    Search and stream matching messages (default).
  list      Same as 'search' but prints summary lines (alias).
  count     Print only the number of matching messages.
  show      Print one message in full (--id, --offset, or --n).
  index     Build a sidecar index for fast repeated queries.
  stats     Print high-level stats (count, size, date range, top senders).
  help      Show this help.

Run 'grepmail <command> --help' for command-specific flags.
`

// Run is the entry point. It returns the desired process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "search", "list":
		return cmdSearch(rest, "summary")
	case "count":
		return cmdSearch(rest, "count")
	case "show":
		return cmdShow(rest)
	case "index":
		return cmdIndex(rest)
	case "stats":
		return cmdStats(rest)
	default:
		fmt.Fprintf(os.Stderr, "grepmail: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func cmdSearch(args []string, defaultFormat string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var ff filterFlags
	ff.register(fs)
	format := fs.String("format", defaultFormat, "summary | json | mbox | raw | count | ids")
	limit := fs.Int("limit", 0, "stop after N matches (0 = unlimited)")
	noIndex := fs.Bool("no-index", false, "skip the sidecar index even if present")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprintln(os.Stderr, "grepmail search [flags] <mbox>\n\nFlags:")
			fs.SetOutput(os.Stderr)
			fs.PrintDefaults()
			return 0
		}
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "grepmail search: expected exactly one mbox path")
		return 2
	}
	spec, err := ff.build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 2
	}
	err = runSearch(runOptions{
		mboxPath: rest[0],
		spec:     spec,
		format:   *format,
		limit:    *limit,
		useIndex: !*noIndex,
		out:      os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}
	return 0
}

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "Message-ID to show")
	n := fs.Int("n", -1, "0-based message index")
	off := fs.Int64("offset", -1, "byte offset of the message in the mbox")
	headersOnly := fs.Bool("headers", false, "print only the header block")
	bodyOnly := fs.Bool("body", false, "print only the decoded body bytes")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "grepmail show [--id ID|--n N|--offset O] <mbox>\n")
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "grepmail show: expected exactly one mbox path")
		return 2
	}
	if *id == "" && *n < 0 && *off < 0 {
		fmt.Fprintln(os.Stderr, "grepmail show: provide --id, --n, or --offset")
		return 2
	}

	path := rest[0]
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}
	defer f.Close()

	// Try the index first.
	if idx, err := index.Load(path); err == nil {
		for i := range idx.Entries {
			e := &idx.Entries[i]
			match := false
			if *id != "" && (e.MessageID == *id || e.MessageID == "<"+*id+">") {
				match = true
			}
			if *n >= 0 && i == *n {
				match = true
			}
			if *off >= 0 && e.Offset == *off {
				match = true
			}
			if match {
				return printOne(f, e.Offset, e.Length, e.HeaderEnd, *headersOnly, *bodyOnly)
			}
		}
	}

	// Streaming fallback.
	f2, sc, err := mbox.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}
	defer f2.Close()
	for sc.Next() {
		m := sc.Message()
		match := false
		if *id != "" && m.MessageID() == *id {
			match = true
		}
		if *n >= 0 && m.Index == *n {
			match = true
		}
		if *off >= 0 && m.Offset == *off {
			match = true
		}
		if match {
			raw, err := m.ReadRaw(f2)
			if err != nil {
				fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
				return 1
			}
			os.Stdout.Write(raw)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "grepmail show: no matching message")
	return 1
}

func printOne(f *os.File, offset, length, headerEnd int64, headersOnly, bodyOnly bool) int {
	raw := make([]byte, length)
	if _, err := f.ReadAt(raw, offset); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}
	switch {
	case headersOnly:
		end := headerEnd
		if end <= 0 || end > int64(len(raw)) {
			end = int64(len(raw))
		}
		os.Stdout.Write(raw[:end])
	case bodyOnly:
		if headerEnd < int64(len(raw)) {
			os.Stdout.Write(raw[headerEnd:])
		}
	default:
		os.Stdout.Write(raw)
	}
	return 0
}

func cmdIndex(args []string) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	verbose := fs.Bool("v", false, "print progress")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "grepmail index [-v] <mbox>")
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "grepmail index: expected exactly one mbox path")
		return 2
	}
	start := time.Now()
	cb := func(n int, b int64) {}
	if *verbose {
		cb = func(n int, b int64) {
			fmt.Fprintf(os.Stderr, "  indexed %d messages (%.1f MB)\n", n, float64(b)/(1024*1024))
		}
	}
	idx, err := index.Build(rest[0], 5000, cb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "grepmail: indexed %d messages in %s → %s\n",
		len(idx.Entries), time.Since(start).Round(time.Millisecond), index.Path(rest[0]))
	return 0
}

func cmdStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	topN := fs.Int("top", 10, "show top-N senders by message count")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "grepmail stats [--top N] <mbox>")
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "grepmail stats: expected exactly one mbox path")
		return 2
	}

	path := rest[0]
	st, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
		return 1
	}

	type stats struct {
		count      int
		earliest   time.Time
		latest     time.Time
		bySender   map[string]int
		withAttach int
		withDate   int
	}
	s := stats{bySender: map[string]int{}}

	walk := func(from string, date time.Time, mt string, hasAttach bool) {
		s.count++
		if !date.IsZero() {
			s.withDate++
			if s.earliest.IsZero() || date.Before(s.earliest) {
				s.earliest = date
			}
			if s.latest.IsZero() || date.After(s.latest) {
				s.latest = date
			}
		}
		if hasAttach {
			s.withAttach++
		}
		if from != "" {
			s.bySender[from]++
		}
	}

	if idx, err := index.Load(path); err == nil {
		for i := range idx.Entries {
			e := &idx.Entries[i]
			walk(e.From, e.Date, e.MIMEType, false)
		}
	} else {
		f, sc, err := mbox.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
			return 1
		}
		defer f.Close()
		for sc.Next() {
			m := sc.Message()
			walk(m.From(), m.Date(), m.Header.Get("Content-Type"), false)
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "grepmail: %v\n", err)
			return 1
		}
	}

	fmt.Printf("file:        %s\n", path)
	fmt.Printf("size:        %.2f MB\n", float64(st.Size())/(1024*1024))
	fmt.Printf("messages:    %d\n", s.count)
	if !s.earliest.IsZero() {
		fmt.Printf("date range:  %s → %s\n",
			s.earliest.Local().Format("2006-01-02"),
			s.latest.Local().Format("2006-01-02"))
	}
	fmt.Printf("with date:   %d\n", s.withDate)
	if *topN > 0 {
		type kv struct {
			k string
			v int
		}
		kvs := make([]kv, 0, len(s.bySender))
		for k, v := range s.bySender {
			kvs = append(kvs, kv{k, v})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
		fmt.Printf("top senders:\n")
		for i := 0; i < *topN && i < len(kvs); i++ {
			fmt.Printf("  %5d  %s\n", kvs[i].v, kvs[i].k)
		}
	}
	return 0
}
