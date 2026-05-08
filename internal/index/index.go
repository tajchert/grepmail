// Package index implements a sidecar index for an mbox file. The index
// stores per-message offsets plus a small set of pre-decoded header fields
// so common queries (date range, from/to/subject substring, message-id
// lookup) can run without parsing the entire mbox.
//
// Format on disk: magic "grepmail-idx\x01", then a gob-encoded File struct.
// The file lives next to the source mbox as <mbox>.grepmail-index. The
// index is invalidated automatically when the mbox's size or modtime
// changes.
package index

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tajchert/grepmail/internal/mbox"
)

const magic = "grepmail-idx\x01"

// Entry is one indexed message.
type Entry struct {
	Offset    int64
	Length    int64
	HeaderEnd int64

	From      string
	To        string
	Cc        string
	Subject   string
	MessageID string
	Date      time.Time
	HasMIME   bool
	MIMEType  string
}

// File is the persisted index payload.
type File struct {
	SourcePath string
	SourceSize int64
	SourceMod  time.Time
	BuiltAt    time.Time
	Version    int
	Entries    []Entry
}

// Path returns the canonical sidecar path for an mbox file.
func Path(mboxPath string) string { return mboxPath + ".grepmail-index" }

// Build scans the mbox at path and writes a fresh index. Progress callbacks
// are invoked roughly once per `progressEvery` messages (zero disables).
func Build(path string, progressEvery int, progress func(count int, bytesRead int64)) (*File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, sc, err := mbox.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	idx := &File{
		SourcePath: path,
		SourceSize: st.Size(),
		SourceMod:  st.ModTime(),
		BuiltAt:    time.Now(),
		Version:    1,
	}

	for sc.Next() {
		m := sc.Message()
		mt, _ := mimeType(m.Header.Get("Content-Type"))
		idx.Entries = append(idx.Entries, Entry{
			Offset:    m.Offset,
			Length:    m.Length,
			HeaderEnd: headerEndOf(m, f),
			From:      m.Header.Get("From"),
			To:        m.Header.Get("To"),
			Cc:        m.Header.Get("Cc"),
			Subject:   m.Subject(),
			MessageID: m.MessageID(),
			Date:      m.Date(),
			MIMEType:  mt,
			HasMIME:   mt != "",
		})
		if progressEvery > 0 && len(idx.Entries)%progressEvery == 0 && progress != nil {
			progress(len(idx.Entries), m.Offset+m.Length)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if err := write(Path(path), idx); err != nil {
		return nil, err
	}
	return idx, nil
}

func headerEndOf(m *mbox.Message, f io.ReaderAt) int64 {
	// Re-derive the headerEnd by reading the message until the blank line.
	const probe = 64 * 1024
	buf := make([]byte, probe)
	n, _ := f.ReadAt(buf, m.Offset)
	buf = buf[:n]
	// Skip the From line.
	nl := bytes.IndexByte(buf, '\n')
	if nl < 0 {
		return m.Length
	}
	rest := buf[nl+1:]
	// Find "\n\n" or "\n\r\n".
	if i := bytes.Index(rest, []byte("\n\n")); i >= 0 {
		return int64(nl + 1 + i + 2)
	}
	if i := bytes.Index(rest, []byte("\n\r\n")); i >= 0 {
		return int64(nl + 1 + i + 3)
	}
	return m.Length
}

func mimeType(ct string) (string, error) {
	if ct == "" {
		return "", nil
	}
	// Strip parameters.
	for i, c := range ct {
		if c == ';' {
			return trim(ct[:i]), nil
		}
	}
	return trim(ct), nil
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// Load reads the index file, validating that it matches the source mbox's
// current size and modtime. Returns (nil, ErrStale) if it doesn't.
func Load(mboxPath string) (*File, error) {
	st, err := os.Stat(mboxPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(Path(mboxPath))
	if err != nil {
		return nil, err
	}
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return nil, fmt.Errorf("index: bad magic")
	}
	var f File
	if err := gob.NewDecoder(bytes.NewReader(data[len(magic):])).Decode(&f); err != nil {
		return nil, err
	}
	if f.SourceSize != st.Size() || !f.SourceMod.Equal(st.ModTime()) {
		return nil, ErrStale
	}
	return &f, nil
}

// ErrStale signals the on-disk index doesn't match the current mbox.
var ErrStale = errors.New("index is stale")

func write(path string, f *File) error {
	var buf bytes.Buffer
	buf.WriteString(magic)
	if err := gob.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
