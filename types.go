package boxtest

// Algorithm identifies the compression codec.
type Algorithm uint8

const (
	AlgoSnappy Algorithm = 0 // Google Snappy
	AlgoZstd   Algorithm = 1 // Facebook Zstd (configurable level)
	AlgoLZ4    Algorithm = 2 // LZ4 block compression
)

func (a Algorithm) String() string {
	switch a {
	case AlgoSnappy:
		return "Snappy"
	case AlgoZstd:
		return "Zstd"
	case AlgoLZ4:
		return "LZ4"
	default:
		return "Unknown"
	}
}

// Zstd compression levels — map to klauspost/compress constants.
const (
	ZstdFastest = 1
	ZstdDefault = 3
	ZstdBetter  = 6
	ZstdBest    = 9
)

const (
	DefaultBlockSize = 64 << 10  // 64 KB
	writeBufSize     = 256 << 10 // bufio.Writer buffer for file output

	magicStr  = "SEGC"
	formatVer = uint8(1)
	headerSz  = 16 // magic(4)+ver(1)+algo(1)+level(1)+blockSize(4)+rsvd(5)
	blkHdrSz  = 8  // per-block inline: compressedSize(4)+originalSize(4)
	idxSlotSz = 16 // index entry: offset(8)+cSize(4)+oSize(4)
	footerSz  = 32 // see format.go
)

// blockInfo is one entry in the block index.
type blockInfo struct {
	CompressedOffset uint64 // absolute file offset of the inline block header
	CompressedSize   uint32 // compressed payload length
	OriginalSize     uint32 // uncompressed length (≤ blockSize)
}
