# SegCompress — Seekable Block Compressor

A Go library for compressing data into seekable, crash-safe block-compressed files and streams. Supports Snappy, Zstd, and LZ4.


## Features

- **Random access** — Seek/Read in original (uncompressed) file coordinates. Only the required blocks are decompressed.
- **Crash-safe streaming write** — Each block flushed immediately with an inline header. Data survives a process crash without Close().
- **Streaming encode/decode** — Works with any `io.Reader` / `io.Writer`. 
- **Pluggable codec** — Snappy (speed), Zstd (4 levels, best ratio), LZ4 (fastest).
- **bufio** — File output uses `bufio.Writer` (256 KB) for efficient small-write batching.
- **Fast open** — Contiguous index read in a single syscall. No scanning of compressed data.

## Installation

```bash
go get github.com/yourorg/segment
```


## Quick start

### Compress & decompress a file

```go
import "github.com/yourorg/segment"

// Compress with Zstd default level, 64 KB blocks.
segment.CompressFile("data.bin", "data.sc", 64<<10, segment.AlgoZstd, segment.ZstdDefault)

// Open and read at arbitrary positions.
cf, _ := segment.OpenCompressedFile("data.sc")
defer cf.Close()

cf.Seek(1000000, io.SeekStart) // seek to offset 1 MB in original file
buf := make([]byte, 4096)
cf.Read(buf)                   // transparent decompress

// ReadAt any position without affecting current position.
cf.ReadAt(buf, 5000000)

// Full decompress.
segment.DecompressFile("data.sc", "restored.bin")
```

### Streaming encode/decode (no files)

```go
// Compress: any io.Reader → any io.Writer
n, _ := segment.CompressStream(dstWriter, srcReader, 64<<10, segment.AlgoLZ4, 0)

// Decompress: any io.Reader → any io.Writer
n, _ := segment.DecompressStream(dstWriter, srcReader)
```

### Streaming write with incremental data

```go
sc, _ := segment.NewStreamCompressor(dstWriter, 64<<10, segment.AlgoZstd, 3)
for _, chunk := range chunks {
    sc.Write(chunk)
}
sc.Close() // writes sentinel, index, footer — output is now a valid compressed file
```

### Streaming read (sequential, no random access)

```go
sr, _ := segment.NewStreamReader(srcReader)
io.Copy(dstWriter, sr) // decompresses on the fly, stops at sentinel
```

## File format

```
┌──────────┬───────────────────────────────┬──────────┬─────────────┬──────────┐
│ Header   │ Blocks                         │ Sentinel │ Index       │ Footer   │
│ 16 B     │ [cSize:4][oSize:4][data] ...   │ 8 B      │ 16 B × N   │ 32 B     │
└──────────┴───────────────────────────────┴──────────┴─────────────┴──────────┘
```

- **Header** (16 B): magic `SEGC`, version, algorithm, level, blockSize
- **Inline block header** (8 B each): compressedSize + originalSize — crash-safe, each block self-describing
- **Sentinel** (8 B): cSize=0 marker, signals end-of-blocks to streaming readers
- **Index** (16 B per block): compressedOffset + compressedSize + originalSize — contiguous, read in one syscall on open
- **Footer** (32 B): numBlocks, originalSize, blockSize, indexOffset, magic — written last

**Crash recovery**: if the process dies before Close(), the footer is missing. On next open, inline block headers are scanned sequentially to rebuild the index. Data is recovered up to the last complete block.

## API reference

### Types

```go
type Algorithm uint8
const (
    AlgoSnappy Algorithm = 0  // Google Snappy
    AlgoZstd   Algorithm = 1  // Facebook Zstd
    AlgoLZ4    Algorithm = 2  // LZ4
)

const DefaultBlockSize = 64 << 10  // 64 KB
```

### Zstd levels

```go
const (
    ZstdFastest = 1   // fastest, lowest ratio
    ZstdDefault = 3   // balanced
    ZstdBetter  = 6   // higher ratio
    ZstdBest    = 9   // best compression
)
```

### File-based API

```go
// Compress an entire file.
func CompressFile(srcPath, dstPath string, blockSize int, algo Algorithm, level int) error

// Decompress an entire file.
func DecompressFile(srcPath, dstPath string) error

// Create a streaming file writer.
func NewCompressedWriter(path string, blockSize int, algo Algorithm, level int) (*CompressedWriter, error)

// Open a compressed file for random-access reading.
func OpenCompressedFile(path string) (*CompressedFile, error)
```

**CompressedWriter** implements `io.WriteCloser` + `io.ReaderFrom`.

**CompressedFile** implements `io.ReadSeeker` + `io.ReaderAt` + `io.Closer`.

```go
func (cf *CompressedFile) Seek(offset int64, whence int) (int64, error)  // original coords
func (cf *CompressedFile) Read(p []byte) (int, error)
func (cf *CompressedFile) ReadAt(p []byte, offset int64) (int, error)
func (cf *CompressedFile) OriginalSize() int64
func (cf *CompressedFile) CleanClose() bool  // true if footer was present
```

### Streaming API (io.Reader / io.Writer)

```go
// One-shot stream compress.
func CompressStream(dst io.Writer, src io.Reader, blockSize int, algo Algorithm, level int) (int64, error)

// One-shot stream decompress.
func DecompressStream(dst io.Writer, src io.Reader) (int64, error)

// Incremental stream compressor.
func NewStreamCompressor(dst io.Writer, blockSize int, algo Algorithm, level int) (*StreamCompressor, error)

// Sequential stream reader (no seeking).
func NewStreamReader(src io.Reader) (*StreamReader, error)
```

**StreamCompressor** implements `io.WriteCloser` + `io.ReaderFrom`. Output is a valid compressed file — can be saved and opened later with `OpenCompressedFile`.

**StreamReader** implements `io.Reader`. Reads and decompresses blocks sequentially; stops at the end-of-blocks sentinel.

## Performance (bigFile.tsb, 88 MB)

```
Algorithm          |  Ratio% |    Comp MB/s | Seek+Read(us)
----------------------------------------------------------------------
Snappy             |    5.1% |     3676 MB/s |        20.14 us
ZstdFastest        |    0.6% |     2449 MB/s |        19.99 us
ZstdDefault        |    0.6% |     2137 MB/s |        17.49 us
ZstdBetter         |    0.6% |      685 MB/s |        26.97 us
ZstdBest           |    0.6% |      527 MB/s |        19.34 us
LZ4                |    0.9% |     3337 MB/s |        13.28 us
```

Block size: 64 KB. Seek+Read: random seek + transparent decompress 4 KB. Measured on Windows 11, Go 1.25.

## Project structure

```
├── types.go           Algorithm enum, format constants, blockInfo
├── format.go          Header, footer, index, block-header serialization
├── compress.go        compressBlock / decompressBlock dispatch
├── writer.go          segWriter (shared core) + CompressedWriter + StreamCompressor
├── reader.go          CompressedFile (random access) + StreamReader (sequential)
├── file.go            CompressFile, DecompressFile convenience wrappers
├── segment_test.go    Unit tests: edge cases, crash recovery, streaming file writes
├── stream_test.go     Streaming io.Reader/io.Writer roundtrip tests
└── bench_test.go      bigFile performance comparison benchmark
```

## License

MIT

## AI generated code