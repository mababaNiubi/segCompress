package boxtest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Binary layout (see README for diagram):
//
//   Header  16 B   magic, version, algo, level, blockSize
//   Blocks  N×     8B inline header + compressed payload
//   Sentinel 8 B    cSize=0 (end-of-blocks marker)
//   Index   N×16 B  contiguous block-offset array (fast-open cache)
//   Footer  32 B    numBlocks, originalSize, indexOffset, magic

// ─── Header ────────────────────────────────────────────────────────

func writeHeader(w io.Writer, algo Algorithm, level, blockSize int) {
	w.Write([]byte(magicStr))
	w.Write([]byte{formatVer, byte(algo), byte(level)})
	putU32LE(w, uint32(blockSize))
	w.Write([]byte{0, 0, 0, 0, 0})
}

func readHeader(r io.Reader) (algo Algorithm, blockSize int, err error) {
	b := make([]byte, headerSz)
	if _, err = io.ReadFull(r, b); err != nil {
		return 0, 0, fmt.Errorf("header: %w", err)
	}
	if string(b[0:4]) != magicStr {
		return 0, 0, fmt.Errorf("bad magic %q", b[0:4])
	}
	if b[4] != formatVer {
		return 0, 0, fmt.Errorf("version %d want %d", b[4], formatVer)
	}
	return Algorithm(b[5]), int(binary.LittleEndian.Uint32(b[7:11])), nil
}

// readHeaderFull reads header and also returns the compression level.
func readHeaderFull(r io.Reader) (algo Algorithm, level int, blockSize int, err error) {
	b := make([]byte, headerSz)
	if _, err = io.ReadFull(r, b); err != nil {
		return 0, 0, 0, fmt.Errorf("header: %w", err)
	}
	if string(b[0:4]) != magicStr {
		return 0, 0, 0, fmt.Errorf("bad magic %q", b[0:4])
	}
	if b[4] != formatVer {
		return 0, 0, 0, fmt.Errorf("version %d want %d", b[4], formatVer)
	}
	return Algorithm(b[5]), int(b[6]), int(binary.LittleEndian.Uint32(b[7:11])), nil
}

// ─── Inline block header ───────────────────────────────────────────

func writeBlkHdr(w io.Writer, cSize, oSize uint32) {
	putU32LE(w, cSize)
	putU32LE(w, oSize)
}

// ─── Index (contiguous, written on Close) ──────────────────────────

func writeIndex(w io.Writer, idx []blockInfo) {
	for i := range idx {
		putU64LE(w, idx[i].CompressedOffset)
		putU32LE(w, idx[i].CompressedSize)
		putU32LE(w, idx[i].OriginalSize)
	}
}

func readIndex(f *os.File, offset int64, n int) ([]blockInfo, error) {
	if n == 0 {
		return nil, nil
	}
	size := n * idxSlotSz
	b := make([]byte, size)
	if _, err := f.ReadAt(b, offset); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	idx := make([]blockInfo, n)
	for i := 0; i < n; i++ {
		o := i * idxSlotSz
		idx[i] = blockInfo{
			CompressedOffset: binary.LittleEndian.Uint64(b[o : o+8]),
			CompressedSize:   binary.LittleEndian.Uint32(b[o+8 : o+12]),
			OriginalSize:     binary.LittleEndian.Uint32(b[o+12 : o+16]),
		}
	}
	return idx, nil
}

// ─── Footer ────────────────────────────────────────────────────────

func writeFooter(w io.Writer, nBlocks int, origSize int64, blockSize int, algo Algorithm, level int, indexOff int64) {
	putU32LE(w, uint32(nBlocks))
	putU64LE(w, uint64(origSize))
	putU32LE(w, uint32(blockSize))
	putU64LE(w, uint64(indexOff))
	w.Write([]byte{byte(algo), byte(level), formatVer, 0})
	w.Write([]byte(magicStr))
}

func parseFooter(f *os.File, fileSize int64, algo Algorithm, blockSize int) (ok bool, nBlocks int, origSize, indexOff int64) {
	if fileSize < int64(headerSz+footerSz) {
		return
	}
	b := make([]byte, footerSz)
	if _, err := f.ReadAt(b, fileSize-footerSz); err != nil {
		return
	}
	if string(b[28:32]) != magicStr {
		return
	}
	nb := int(binary.LittleEndian.Uint32(b[0:4]))
	os := int64(binary.LittleEndian.Uint64(b[4:12]))
	bs := int(binary.LittleEndian.Uint32(b[12:16]))
	io := int64(binary.LittleEndian.Uint64(b[16:24]))
	a := Algorithm(b[24])
	if a != algo || bs != blockSize {
		return
	}
	return true, nb, os, io
}

// ─── Little-endian helpers ─────────────────────────────────────────

func putU32LE(w io.Writer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.Write(b[:])
}

func putU64LE(w io.Writer, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.Write(b[:])
}

func u32LE(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
