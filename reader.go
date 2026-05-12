package boxtest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ─── CompressedFile (random-access, file-based) ────────────────────
//
// Implements io.ReadSeeker, io.ReaderAt, io.Closer.  Seek/Read operate
// in original (uncompressed) coordinates.  On open, the contiguous
// index is read in one syscall.  If the file was not cleanly closed,
// the index is rebuilt by scanning inline block headers.

type CompressedFile struct {
	f            *os.File
	algo         Algorithm
	blockSize    int
	numBlocks    int
	originalSize int64
	index        []blockInfo
	cleanClose   bool

	pos int64 // current Read/Seek position (original coords)

	cacheIdx  int
	cacheData []byte
	cacheErr  error

	zstdDec *zstd.Decoder
}

// OpenCompressedFile opens a compressed file for random-access reading.
func OpenCompressedFile(path string) (*CompressedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	cf := &CompressedFile{f: f, cacheIdx: -1}
	if err := cf.init(); err != nil {
		f.Close()
		return nil, err
	}
	return cf, nil
}

func (cf *CompressedFile) init() error {
	algo, blockSize, err := readHeader(cf.f)
	if err != nil {
		return err
	}
	cf.algo = algo
	cf.blockSize = blockSize

	fi, err := cf.f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	fileSize := fi.Size()

	// Clean close: read contiguous index.
	if ok, nb, orig, idxOff := parseFooter(cf.f, fileSize, cf.algo, cf.blockSize); ok {
		cf.cleanClose = true
		cf.numBlocks = nb
		cf.originalSize = orig
		cf.index, err = readIndex(cf.f, idxOff, nb)
		return err
	}

	// Crash recovery: scan inline headers.
	cf.cleanClose = false
	return cf.scanRecover(fileSize)
}

func (cf *CompressedFile) scanRecover(fileSize int64) error {
	pos := int64(headerSz)
	cf.index = nil
	cf.numBlocks = 0
	cf.originalSize = 0
	for {
		if pos+int64(blkHdrSz) > fileSize {
			break
		}
		bh := make([]byte, blkHdrSz)
		if _, err := cf.f.ReadAt(bh, pos); err != nil {
			break
		}
		cSize := binary.LittleEndian.Uint32(bh[0:4])
		oSize := binary.LittleEndian.Uint32(bh[4:8])
		if cSize == 0 {
			break // sentinel
		}
		if pos+int64(blkHdrSz)+int64(cSize) > fileSize {
			break
		}
		cf.index = append(cf.index, blockInfo{
			CompressedOffset: uint64(pos), CompressedSize: cSize, OriginalSize: oSize,
		})
		cf.numBlocks++
		cf.originalSize += int64(oSize)
		pos += int64(blkHdrSz) + int64(cSize)
	}
	return nil
}

func (cf *CompressedFile) blockIdx(origOff int64) int {
	return int(origOff / int64(cf.blockSize))
}

func (cf *CompressedFile) loadBlock(idx int) {
	if idx == cf.cacheIdx && cf.cacheErr == nil {
		return
	}
	cf.cacheIdx = idx
	cf.cacheData = nil
	cf.cacheErr = nil
	if idx < 0 || idx >= cf.numBlocks {
		cf.cacheErr = fmt.Errorf("block %d out of range [0,%d)", idx, cf.numBlocks)
		return
	}
	bi := cf.index[idx]
	compressed := make([]byte, bi.CompressedSize)
	if _, err := cf.f.ReadAt(compressed, int64(bi.CompressedOffset)+blkHdrSz); err != nil {
		cf.cacheErr = fmt.Errorf("read block %d: %w", idx, err)
		return
	}
	data, err := decompressBlock(compressed, int(bi.OriginalSize), cf.algo, &cf.zstdDec)
	if err != nil {
		cf.cacheErr = fmt.Errorf("decompress block %d: %w", idx, err)
		return
	}
	cf.cacheData = data
}

// Read reads up to len(p) bytes at the current original position.
func (cf *CompressedFile) Read(p []byte) (n int, err error) {
	if cf.pos >= cf.originalSize {
		return 0, io.EOF
	}
	for len(p) > 0 && cf.pos < cf.originalSize {
		bi := cf.blockIdx(cf.pos)
		cf.loadBlock(bi)
		if cf.cacheErr != nil {
			return n, cf.cacheErr
		}
		off := int(cf.pos % int64(cf.blockSize))
		c := copy(p, cf.cacheData[off:])
		p = p[c:]
		cf.pos += int64(c)
		n += c
	}
	if n == 0 && cf.pos >= cf.originalSize {
		return 0, io.EOF
	}
	return n, nil
}

// Seek moves the read position in original coordinates.
func (cf *CompressedFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = cf.pos + offset
	case io.SeekEnd:
		abs = cf.originalSize + offset
	default:
		return 0, fmt.Errorf("bad whence %d", whence)
	}
	if abs < 0 {
		abs = 0
	}
	if abs > cf.originalSize {
		abs = cf.originalSize
	}
	cf.pos = abs
	return cf.pos, nil
}

// ReadAt reads len(p) bytes at the given original offset.
// Does not affect the current position.
func (cf *CompressedFile) ReadAt(p []byte, offset int64) (int, error) {
	if offset >= cf.originalSize {
		return 0, io.EOF
	}
	n := 0
	for len(p) > 0 && offset < cf.originalSize {
		bi := cf.blockIdx(offset)
		if bi != cf.cacheIdx {
			cf.loadBlock(bi)
			if cf.cacheErr != nil {
				return n, cf.cacheErr
			}
		}
		off := int(offset % int64(cf.blockSize))
		c := copy(p, cf.cacheData[off:])
		p = p[c:]
		offset += int64(c)
		n += c
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (cf *CompressedFile) Close() error {
	cf.zstdDec = nil
	cf.cacheData = nil
	return cf.f.Close()
}

func (cf *CompressedFile) OriginalSize() int64  { return cf.originalSize }
func (cf *CompressedFile) Algorithm() Algorithm { return cf.algo }
func (cf *CompressedFile) CleanClose() bool     { return cf.cleanClose }

// ─── StreamReader (sequential, io.Reader source) ───────────────────
//
// Reads and decompresses compressed data from any io.Reader.
// No seeking — blocks are decompressed on the fly as the caller reads.
// Stops when the end-of-blocks sentinel (cSize=0) is reached.

type StreamReader struct {
	src       io.Reader
	algo      Algorithm
	blockSize int

	buf    []byte // current decompressed block
	bufPos int

	zstdDec *zstd.Decoder
	done    bool
}

// NewStreamReader reads the header from src and returns a streaming
// decompressor.  Read() returns original data sequentially until EOF.
func NewStreamReader(src io.Reader) (*StreamReader, error) {
	algo, blockSize, err := readHeader(src)
	if err != nil {
		return nil, err
	}
	return &StreamReader{src: src, algo: algo, blockSize: blockSize}, nil
}

func (sr *StreamReader) Read(p []byte) (int, error) {
	for sr.bufPos >= len(sr.buf) {
		if sr.done {
			return 0, io.EOF
		}
		if err := sr.nextBlock(); err != nil {
			return 0, err
		}
	}
	n := copy(p, sr.buf[sr.bufPos:])
	sr.bufPos += n
	return n, nil
}

func (sr *StreamReader) nextBlock() error {
	var bh [blkHdrSz]byte
	if _, err := io.ReadFull(sr.src, bh[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			sr.done = true
			return io.EOF
		}
		return fmt.Errorf("block header: %w", err)
	}
	cSize := binary.LittleEndian.Uint32(bh[0:4])
	oSize := binary.LittleEndian.Uint32(bh[4:8])
	if cSize == 0 { // sentinel
		sr.done = true
		return io.EOF
	}
	compressed := make([]byte, cSize)
	if _, err := io.ReadFull(sr.src, compressed); err != nil {
		return fmt.Errorf("block data: %w", err)
	}
	data, err := decompressBlock(compressed, int(oSize), sr.algo, &sr.zstdDec)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	sr.buf = data
	sr.bufPos = 0
	return nil
}

// DecompressStream reads compressed data from src, decompresses it,
// and writes the original data to dst.  Returns bytes written to dst.
func DecompressStream(dst io.Writer, src io.Reader) (int64, error) {
	sr, err := NewStreamReader(src)
	if err != nil {
		return 0, err
	}
	return io.Copy(dst, sr)
}
