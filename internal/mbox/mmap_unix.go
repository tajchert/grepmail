//go:build unix

package mbox

import (
	"io"
	"os"
	"syscall"
)

// MappedFile is a read-only memory-mapped view of an mbox file. Slice()
// returns zero-copy views into the mapping; the bytes are valid until
// Close. ReadAt is provided so it satisfies io.ReaderAt and can stand in
// for *os.File anywhere we previously passed one.
type MappedFile struct {
	f    *os.File
	data []byte
	size int64
}

// OpenMapped memory-maps path for read. On platforms without mmap support
// this falls back to a plain *os.File wrapper (see mmap_other.go).
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
	size := st.Size()
	if size == 0 {
		return &MappedFile{f: f}, nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &MappedFile{f: f, data: data, size: size}, nil
}

// Size returns the mapped file size in bytes.
func (m *MappedFile) Size() int64 { return m.size }

// Slice returns a zero-copy view of [off, off+length). The returned slice
// is valid only until Close. Out-of-range requests are clamped to the
// mapped region.
func (m *MappedFile) Slice(off, length int64) []byte {
	if off < 0 || off >= m.size || length <= 0 {
		return nil
	}
	end := off + length
	if end > m.size {
		end = m.size
	}
	return m.data[off:end]
}

// ReadAt copies into p so MappedFile satisfies io.ReaderAt.
func (m *MappedFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= m.size {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if int64(off)+int64(n) >= m.size && n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Close unmaps and closes the underlying file.
func (m *MappedFile) Close() error {
	var mErr error
	if m.data != nil {
		mErr = syscall.Munmap(m.data)
		m.data = nil
	}
	fErr := m.f.Close()
	if mErr != nil {
		return mErr
	}
	return fErr
}
