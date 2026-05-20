package boxtest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ─── Shared compression core ───────────────────────────────────────
//
// segWriter handles buffering, compression and block output.  Both
// CompressedWriter (file) and StreamCompressor (io.Writer) use it.

type segWriter struct {
	w      io.Writer
	curOff int64 // running write position (for index)

	blockSize int
	algo      Algorithm
	level     int

	abuf []byte // accumulation buffer (size = blockSize)
	an   int    // bytes accumulated

	index []blockInfo

	zstdEnc *zstd.Encoder

	origSize int64
	closed   bool
}

func newSegWriter(w io.Writer, blockSize int, algo Algorithm, level int) *segWriter {
	writeHeader(w, algo, level, blockSize)
	return &segWriter{
		w:         w,
		curOff:    headerSz,
		blockSize: blockSize,
		algo:      algo,
		level:     level,
		abuf:      make([]byte, blockSize),
	}
}

// write buffers p and flushes full blocks to w.
func (s *segWriter) write(p []byte) (int, error) {
	total := len(p)
	s.origSize += int64(total)
	for len(p) > 0 {
		n := copy(s.abuf[s.an:], p)
		s.an += n
		p = p[n:]
		if s.an == s.blockSize {
			if err := s.flush(); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

// flush compresses the accumulated block and writes it.
func (s *segWriter) flush() error {
	compressed, err := compressBlock(s.abuf[:s.an], s.algo, s.level, &s.zstdEnc)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	cSize := uint32(len(compressed))
	writeBlkHdr(s.w, cSize)
	if _, err := s.w.Write(compressed); err != nil {
		return err
	}
	s.index = append(s.index, blockInfo{
		CompressedOffset: uint64(s.curOff),
		CompressedSize:   cSize,
	})
	s.curOff += int64(blkHdrSz + len(compressed))
	s.an = 0
	return nil
}

// finalize writes sentinel, index, and footer.  After this the output
// is a valid compressed file.
func (s *segWriter) finalize() error {
	// Flush partial final block.
	if s.an > 0 {
		if err := s.flush(); err != nil {
			return err
		}
	}
	// End-of-blocks sentinel.
	writeBlkHdr(s.w, 0)
	s.curOff += blkHdrSz
	// Contiguous index.
	indexOff := s.curOff
	writeIndex(s.w, s.index)
	// Footer.
	writeFooter(s.w, len(s.index), s.origSize, s.blockSize, s.algo, s.level, indexOff)
	return nil
}

// ─── CompressedWriter (file target) ────────────────────────────────

// CompressedWriter compresses data into a file.  Uses bufio for
// efficient small writes (header, block headers, index).  Data blocks
// are flushed immediately — crash-safe.
type CompressedWriter struct {
	sw   *segWriter
	file *os.File
	bw   *bufio.Writer // buffered wrapper around file
}

// NewCompressedWriter creates a file and prepares it for compressed writes.
func NewCompressedWriter(path string, blockSize int, algo Algorithm, level int) (*CompressedWriter, error) {
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	// bufio.Writer batches small writes (header, block headers, footer) into
	// fewer syscalls.  Flushed before each file.Seek (position tracking).
	bw := bufio.NewWriterSize(f, writeBufSize)
	sw := newSegWriter(bw, blockSize, algo, level)
	return &CompressedWriter{sw: sw, file: f, bw: bw}, nil
}

// ResumeCompressedWriter opens an existing compressed file for continued
// writing.  If the file was not cleanly closed (missing or corrupt footer),
// it scans inline block headers, truncates any trailing incomplete data, and
// resumes from the last complete block.  Returns an error if the file was
// cleanly closed (footer present) — use NewCompressedWriter to overwrite.
func ResumeCompressedWriter(path string) (*CompressedWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	algo, level, blockSize, err := readHeaderFull(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat: %w", err)
	}
	fileSize := fi.Size()

	// If footer is valid the file was cleanly closed — nothing to recover.
	if ok, _, _, _ := parseFooter(f, fileSize, algo, blockSize); ok {
		f.Close()
		return nil, fmt.Errorf("file was cleanly closed, use NewCompressedWriter to overwrite")
	}

	index, endPos := scanBlockIndex(f, fileSize)

	// Compute original size from recovered blocks.
	origSize := int64(0)
	if len(index) > 0 {
		lastSize, err := decompressedBlockSize(f, index[len(index)-1], blockSize, algo)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("recover last block: %w", err)
		}
		origSize = int64(len(index)-1)*int64(blockSize) + int64(lastSize)
	}
	curOff := endPos

	if err := f.Truncate(curOff); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncate: %w", err)
	}
	if _, err := f.Seek(curOff, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek: %w", err)
	}

	bw := bufio.NewWriterSize(f, writeBufSize)
	sw := &segWriter{
		w:         bw,
		curOff:    curOff,
		blockSize: blockSize,
		algo:      algo,
		level:     level,
		abuf:      make([]byte, blockSize),
		index:     index,
		origSize:  origSize,
	}

	return &CompressedWriter{sw: sw, file: f, bw: bw}, nil
}

func (w *CompressedWriter) Write(p []byte) (int, error) {
	if w.sw.closed {
		return 0, errors.New("writer closed")
	}
	return w.sw.write(p)
}

// ReadFrom enables efficient io.Copy.
func (w *CompressedWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.sw.closed {
		return 0, errors.New("writer closed")
	}
	var total int64
	buf := make([]byte, 128<<10)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}

// Close flushes the remaining block, writes sentinel, index and footer,
// then closes the file.
func (w *CompressedWriter) Close() error {
	if w.sw.closed {
		return nil
	}
	w.sw.closed = true

	// Flush bufio so file position is accurate before finalize writes index.
	if err := w.bw.Flush(); err != nil {
		w.file.Close()
		return err
	}
	// Get accurate file position for index.
	pos, _ := w.file.Seek(0, io.SeekCurrent)
	w.sw.curOff = pos

	if err := w.sw.finalize(); err != nil {
		w.file.Close()
		return err
	}
	if err := w.bw.Flush(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

func (w *CompressedWriter) NumBlocks() int { return len(w.sw.index) }

// ─── StreamCompressor (io.Writer target) ───────────────────────────

// StreamCompressor compresses data to any io.Writer.  No temporary
// files — writes header immediately, blocks as they arrive, and
// sentinel + index + footer on Close.  Output is a valid compressed
// file (can be saved and opened later with OpenCompressedFile).
type StreamCompressor struct {
	sw     *segWriter
	closed bool
}

// NewStreamCompressor creates a streaming compressor writing to dst.
func NewStreamCompressor(dst io.Writer, blockSize int, algo Algorithm, level int) (*StreamCompressor, error) {
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	return &StreamCompressor{sw: newSegWriter(dst, blockSize, algo, level)}, nil
}

func (s *StreamCompressor) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("writer closed")
	}
	return s.sw.write(p)
}

func (s *StreamCompressor) ReadFrom(r io.Reader) (int64, error) {
	if s.closed {
		return 0, errors.New("writer closed")
	}
	var total int64
	buf := make([]byte, 128<<10)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := s.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}

// Close flushes any remaining data, writes sentinel, index and footer.
// Does NOT close the underlying io.Writer.
func (s *StreamCompressor) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.sw.finalize()
}

// ─── CompressStream convenience ────────────────────────────────────

// CompressStream reads all data from src, compresses it, and writes the
// result to dst.  Returns the number of original bytes compressed.
func CompressStream(dst io.Writer, src io.Reader, blockSize int, algo Algorithm, level int) (int64, error) {
	sc, err := NewStreamCompressor(dst, blockSize, algo, level)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(sc, src)
	if err != nil {
		return n, err
	}
	return n, sc.Close()
}
