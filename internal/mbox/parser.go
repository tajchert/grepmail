// Package mbox provides a streaming parser for mbox-format mailbox files
// (RFC 4155 / mboxrd-ish, with tolerance for the variants Gmail and other
// real-world tools emit).
//
// The parser is built around a single observation: every message starts with
// a line beginning with "From " at byte offset 0 of the file or immediately
// after a blank line. We scan for that boundary and hand each message back
// as an offset+length pair into the underlying file. Headers are parsed
// lazily; the body is never copied unless the caller asks for it.
package mbox

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"os"
	"strings"
	"time"
)

// Message is a lightweight handle to a single message inside an mbox file.
// Offset is the byte position of the leading "From " separator; Length is
// the number of bytes up to (but not including) the next separator. The
// header block is pre-parsed (cheap, bounded) but the body is not.
type Message struct {
	Offset    int64
	Length    int64
	Index     int // 0-based position within the mbox
	Header    mail.Header
	headerEnd int64 // offset (relative to Offset) where headers end
}

// From returns the first From: address as a display string, or "".
func (m *Message) From() string { return m.Header.Get("From") }

// To returns the To: header as-is.
func (m *Message) To() string { return m.Header.Get("To") }

// Subject returns the (RFC 2047-decoded) subject.
func (m *Message) Subject() string {
	dec := new(mime.WordDecoder)
	s, err := dec.DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		return m.Header.Get("Subject")
	}
	return s
}

// Date parses the Date: header. Returns zero time on failure.
func (m *Message) Date() time.Time {
	t, err := mail.ParseDate(m.Header.Get("Date"))
	if err != nil {
		return time.Time{}
	}
	return t
}

// MessageID returns the Message-ID header value with surrounding <> stripped.
func (m *Message) MessageID() string {
	id := m.Header.Get("Message-ID")
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return id
}

// ReadRaw returns the full raw bytes of the message (including the leading
// "From " line). The caller owns the returned slice.
func (m *Message) ReadRaw(f io.ReaderAt) ([]byte, error) {
	buf := make([]byte, m.Length)
	_, err := f.ReadAt(buf, m.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// ReadBody returns the body bytes (after the blank line separating headers
// from body). The caller owns the returned slice.
func (m *Message) ReadBody(f io.ReaderAt) ([]byte, error) {
	bodyLen := m.Length - m.headerEnd
	if bodyLen <= 0 {
		return nil, nil
	}
	buf := make([]byte, bodyLen)
	_, err := f.ReadAt(buf, m.Offset+m.headerEnd)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// ReadHeaderBytes returns the raw header block (excluding the leading "From "
// envelope line, including the trailing CRLF before the body).
func (m *Message) ReadHeaderBytes(f io.ReaderAt) ([]byte, error) {
	// We don't have the From-line length stored separately; recompute by
	// reading enough of the message to find the first newline.
	const probe = 1024
	head := make([]byte, probe)
	n, _ := f.ReadAt(head, m.Offset)
	head = head[:n]
	nl := bytes.IndexByte(head, '\n')
	if nl < 0 {
		return nil, nil
	}
	start := int64(nl + 1)
	if m.headerEnd <= start {
		return nil, nil
	}
	buf := make([]byte, m.headerEnd-start)
	_, err := f.ReadAt(buf, m.Offset+start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// Scanner walks an mbox file message-by-message. It is single-pass and
// forward-only; build an Index if you need random access.
type Scanner struct {
	r   *bufio.Reader
	pos int64 // absolute byte offset of next byte to read
	idx int

	pendingFromLine []byte
	pendingFromAt   int64
	done            bool
	err             error
	cur             *Message
}

// NewScanner creates a Scanner over r. Buffer is sized for fast linear scans.
func NewScanner(r io.Reader) *Scanner {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 1<<20)
	}
	return &Scanner{r: br}
}

// Open is a convenience wrapper for scanning a file path.
func Open(path string) (*os.File, *Scanner, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, NewScanner(bufio.NewReaderSize(f, 1<<20)), nil
}

// fromLinePrefix is the byte sequence at the start of every message envelope.
var fromLinePrefix = []byte("From ")

// readLine reads one line (including trailing \n) from the underlying reader,
// updating pos. It returns the line bytes (slice valid until next call) and
// any error. At EOF it returns (line-or-nil, io.EOF).
func (s *Scanner) readLine() ([]byte, error) {
	line, err := s.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		// Long line; fall back to ReadBytes which allocates.
		head := append([]byte(nil), line...)
		rest, e2 := s.r.ReadBytes('\n')
		head = append(head, rest...)
		s.pos += int64(len(head))
		return head, e2
	}
	s.pos += int64(len(line))
	return line, err
}

// Next advances to the next message. Returns false at EOF or on error.
func (s *Scanner) Next() bool {
	if s.done || s.err != nil {
		return false
	}

	var fromAt int64

	if s.pendingFromLine != nil {
		fromAt = s.pendingFromAt
		s.pendingFromLine = nil
	} else {
		// First message: skip until we find a "From " line at start of file.
		for {
			at := s.pos
			line, err := s.readLine()
			if len(line) > 0 && bytes.HasPrefix(line, fromLinePrefix) {
				fromAt = at
				break
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.err = err
				}
				s.done = true
				return false
			}
		}
	}

	// Read header lines up to a blank line.
	var headerBuf bytes.Buffer
	headerEnd := int64(0)
	for {
		at := s.pos
		line, err := s.readLine()
		if len(line) == 0 && err != nil {
			headerEnd = at - fromAt
			break
		}
		// Blank line terminates headers.
		if len(line) == 1 && line[0] == '\n' {
			headerBuf.Write(line)
			headerEnd = s.pos - fromAt
			break
		}
		if len(line) == 2 && line[0] == '\r' && line[1] == '\n' {
			headerBuf.Write(line)
			headerEnd = s.pos - fromAt
			break
		}
		headerBuf.Write(line)
		if err != nil {
			headerEnd = s.pos - fromAt
			break
		}
	}

	// Read body until next "From " line at start of line preceded by blank.
	// Convention: a "From " line at the absolute start of a line (after \n)
	// where the previous line is blank (or EOF) is a separator.
	var prevBlank bool = true // header/body separator counts as blank-ish
	var endOfMessage int64 = s.pos
	for {
		at := s.pos
		line, err := s.readLine()
		if len(line) == 0 {
			endOfMessage = at
			s.done = true
			break
		}
		if prevBlank && bytes.HasPrefix(line, fromLinePrefix) {
			// Boundary. Stash this line for the next iteration.
			s.pendingFromLine = append([]byte(nil), line...)
			s.pendingFromAt = at
			endOfMessage = at
			break
		}
		if len(line) == 1 && line[0] == '\n' {
			prevBlank = true
		} else if len(line) == 2 && line[0] == '\r' && line[1] == '\n' {
			prevBlank = true
		} else {
			prevBlank = false
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.err = err
			}
			endOfMessage = s.pos
			s.done = true
			break
		}
	}

	// Parse header block.
	hdr, err := parseHeader(headerBuf.Bytes())
	if err != nil {
		// Tolerate malformed headers — return what we got.
		hdr = mail.Header{}
	}

	s.cur = &Message{
		Offset:    fromAt,
		Length:    endOfMessage - fromAt,
		Index:     s.idx,
		Header:    hdr,
		headerEnd: headerEnd,
	}
	s.idx++
	return true
}

// Message returns the most recently scanned Message (valid until Next is
// called again).
func (s *Scanner) Message() *Message { return s.cur }

// Err returns the first error encountered during scanning.
func (s *Scanner) Err() error { return s.err }

func parseHeader(b []byte) (mail.Header, error) {
	if len(b) == 0 {
		return mail.Header{}, nil
	}
	// net/mail expects a body after the headers; tack on an empty one.
	tp := bufio.NewReader(io.MultiReader(bytes.NewReader(b), strings.NewReader("\r\n")))
	msg, err := mail.ReadMessage(tp)
	if err != nil {
		return mail.Header{}, fmt.Errorf("parse header: %w", err)
	}
	return msg.Header, nil
}
