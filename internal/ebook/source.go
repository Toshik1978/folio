package ebook

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sync"
)

// inflateChunk is the most an archive entry inflates per decompressor read, so
// a header-only read of a large entry costs a few KB, not the whole entry.
const inflateChunk = 32 << 10 // 32 KiB

// maxEmptyReads bounds consecutive (0, nil) reads from a decompressor before
// the entry is treated as malformed rather than spinning forever.
const maxEmptyReads = 100

// errNegativeOffset is returned for a ReadAt at a negative offset.
var errNegativeOffset = errors.New("ebook: negative read offset")

// Source is the random-access view of one book file that every parser reads.
// Name carries the file name (its extension selects the parser); R serves the
// bytes [0, Size).
type Source struct {
	Name string
	R    io.ReaderAt
	Size int64
}

// OpenFile opens a plain file read-only. R is TagIO over the *os.File, so its
// read errors match ErrSourceIO; Size comes from Stat. Open and Stat errors
// keep their cause in the chain (for example fs.ErrNotExist) and do not match
// ErrSourceIO. The caller closes the
// returned Closer.
func OpenFile(name string) (Source, io.Closer, error) {
	f, err := os.Open(name)
	if err != nil {
		return Source{}, nil, fmt.Errorf("ebook: open source: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return Source{}, nil, fmt.Errorf("ebook: stat source: %w", err)
	}

	return Source{Name: filepath.Base(name), R: TagIO(f), Size: fi.Size()}, f, nil
}

// TagIO wraps r so that every read error other than io.EOF matches
// ErrSourceIO, keeping the original error in the chain.
func TagIO(r io.ReaderAt) io.ReaderAt { return taggedReaderAt{r: r} }

// taggedReaderAt is the io.ReaderAt returned by TagIO.
type taggedReaderAt struct{ r io.ReaderAt }

// ReadAt implements io.ReaderAt.
func (t taggedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := t.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrSourceIO) {
		err = fmt.Errorf("%w: %w", ErrSourceIO, err)
	}

	return n, err
}

// OpenZipEntry returns a Source over a zip entry that inflates lazily: the
// decompressor opens on the first ReadAt and inflates sequentially only up to
// the furthest offset read, keeping the inflated prefix in memory so backward
// reads never reopen it. Holding more than limit bytes returns ErrTooLarge.
// A stream that ends before the declared size, or a flate or checksum error,
// returns ErrMalformed, unless the underlying error matches ErrSourceIO.
// Production passes MaxEntryBytes; tests pass a small limit. R is safe for
// concurrent ReadAt. The returned Closer releases the decompressor and the
// buffer.
func OpenZipEntry(f *zip.File, limit int64) (Source, io.Closer) {
	e := &lazyEntry{f: f, limit: limit, size: math.MaxInt64}
	if f.UncompressedSize64 < math.MaxInt64 {
		e.size = int64(f.UncompressedSize64)
	}

	return Source{Name: path.Base(f.Name), R: e, Size: e.size}, e
}

// lazyEntry is the mutex-guarded lazy reader behind OpenZipEntry.
type lazyEntry struct {
	mu     sync.Mutex
	f      *zip.File
	limit  int64
	size   int64
	rc     io.ReadCloser
	buf    []byte // the inflated prefix of the entry
	err    error  // sticky terminal error of the decompressor
	done   bool   // the stream ended cleanly at the declared size
	opens  int    // decompressor opens, for tests
	closed bool
}

// ReadAt implements io.ReaderAt.
func (e *lazyEntry) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errNegativeOffset
	}
	if off >= e.size {
		return 0, io.EOF
	}
	want := min(off+int64(len(p)), e.size)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0, fs.ErrClosed
	}

	err := e.fill(want)
	n := 0
	if off < int64(len(e.buf)) {
		n = copy(p, e.buf[off:min(want, int64(len(e.buf)))])
	}
	if err == nil && n < len(p) {
		err = io.EOF // the read was clamped at Size
	}

	return n, err
}

// Close implements io.Closer, releasing the decompressor and the buffer.
func (e *lazyEntry) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.buf = nil
	if e.rc == nil {
		return nil
	}
	err := e.rc.Close()
	e.rc = nil
	if err != nil {
		return fmt.Errorf("ebook: close entry %q: %w", e.f.Name, err)
	}

	return nil
}

// fill makes the buffer cover want bytes, unless the limit is reached or the
// stream fails. It reports an error only when want is not covered, or when want
// reaches the declared size and the stream's end failed verification: reaching
// the size also reads to the stream's end, so the zip checksum is checked
// before the final bytes are served.
func (e *lazyEntry) fill(want int64) error {
	if want > e.limit {
		if err := e.fill(e.limit); err != nil {
			return err
		}

		return fmt.Errorf("%w: entry %q is larger than %d bytes", ErrTooLarge, e.f.Name, e.limit)
	}
	if err := e.inflateTo(want); err != nil {
		return err
	}
	if want < e.size {
		return nil
	}
	if e.err == nil && !e.done {
		e.verifyEnd()
	}

	return e.err
}

// inflateTo inflates sequentially until the buffer holds want bytes, returning
// the stream's terminal error if it stops short.
func (e *lazyEntry) inflateTo(want int64) error {
	empty := 0
	for int64(len(e.buf)) < want && !e.done {
		if e.err != nil {
			return e.err
		}
		if e.rc == nil {
			if err := e.open(); err != nil {
				return err
			}
		}
		if e.readChunk(want) > 0 {
			empty = 0
			continue
		}
		empty++
		if empty >= maxEmptyReads && e.rc != nil {
			e.finish(io.ErrNoProgress)
		}
	}

	return nil
}

// readChunk inflates at most inflateChunk bytes, never past want, onto the
// buffer and returns how many it got. A read error ends the stream.
func (e *lazyEntry) readChunk(want int64) int {
	chunk := int(min(inflateChunk, want-int64(len(e.buf))))
	e.grow(len(e.buf) + chunk)
	n, err := e.rc.Read(e.buf[len(e.buf) : len(e.buf)+chunk])
	e.buf = e.buf[:len(e.buf)+n]
	if err != nil {
		e.finish(err)
	}

	return n
}

// open starts the decompressor.
func (e *lazyEntry) open() error {
	rc, err := e.f.Open()
	if err != nil {
		e.err = classifyInflate(err)
		return e.err
	}
	e.rc = rc
	e.opens++

	return nil
}

// verifyEnd reads past the declared size: the stream must end there, and the
// zip reader checks the CRC as it reports the end.
func (e *lazyEntry) verifyEnd() {
	var probe [1]byte
	for range maxEmptyReads {
		n, err := e.rc.Read(probe[:])
		switch {
		case n > 0:
			e.finish(fmt.Errorf("%w: entry %q inflates past its declared size", ErrMalformed, e.f.Name))
			return
		case err != nil:
			e.finish(err)
			return
		}
	}
	e.finish(io.ErrNoProgress)
}

// finish records the decompressor's terminal error and releases it. A clean
// end of stream at the declared size marks the entry done instead.
func (e *lazyEntry) finish(err error) {
	switch {
	case !errors.Is(err, io.EOF):
		e.err = classifyInflate(err)
	case int64(len(e.buf)) < e.size:
		e.err = fmt.Errorf("%w: entry %q ended at %d of %d bytes", ErrMalformed, e.f.Name, len(e.buf), e.size)
	}
	_ = e.rc.Close()
	e.rc = nil
	e.done = e.err == nil
}

// grow ensures the buffer has capacity for n bytes. Capacity doubles, but never
// past the most the entry may hold (its declared size or the limit), so a
// buffer near the limit does not briefly reserve twice the limit.
func (e *lazyEntry) grow(n int) {
	if cap(e.buf) >= n {
		return
	}
	ceiling := int(min(e.size, e.limit))
	next := max(n, min(2*cap(e.buf), ceiling))
	buf := make([]byte, len(e.buf), next)
	copy(buf, e.buf)
	e.buf = buf
}

// inflated reports how many bytes have been inflated so far (for tests).
func (e *lazyEntry) inflated() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()

	return int64(len(e.buf))
}

// classifyInflate maps a decompressor error to ErrMalformed, leaving errors of
// the underlying source (ErrSourceIO) as they are.
func classifyInflate(err error) error {
	if errors.Is(err, ErrSourceIO) || errors.Is(err, ErrMalformed) {
		return err
	}

	return fmt.Errorf("%w: %w", ErrMalformed, err)
}
