//go:build !unix

package mbox

import (
	"io"
	"os"
)

// MappedFile is a *os.File-backed fallback used on platforms without
// syscall.Mmap. Slice copies; the API is identical to the unix build so
// callers don't need build tags.
type MappedFile struct {
	f    *os.File
	size int64
}

func OpenMapped(path string) (*MappedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &MappedFile{f: f, size: st.Size()}, nil
}

func (m *MappedFile) Size() int64 { return m.size }

func (m *MappedFile) Slice(off, length int64) []byte {
	if off < 0 || off >= m.size || length <= 0 {
		return nil
	}
	end := off + length
	if end > m.size {
		end = m.size
	}
	buf := make([]byte, end-off)
	n, err := m.f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil
	}
	return buf[:n]
}

func (m *MappedFile) ReadAt(p []byte, off int64) (int, error) {
	return m.f.ReadAt(p, off)
}

func (m *MappedFile) Close() error { return m.f.Close() }
