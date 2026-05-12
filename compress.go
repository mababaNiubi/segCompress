package boxtest

import (
	"fmt"

	"github.com/golang/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// compressBlock dispatches to the selected codec.  zstdEnc and lz4HT are
// lazy-allocated and reused across calls within a single file / stream.
func compressBlock(data []byte, algo Algorithm, level int, zstdEnc **zstd.Encoder, lz4HT *[]int) ([]byte, error) {
	switch algo {
	case AlgoSnappy:
		dst := make([]byte, snappy.MaxEncodedLen(len(data)))
		return snappy.Encode(dst, data), nil

	case AlgoZstd:
		if *zstdEnc == nil {
			enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
			if err != nil {
				return nil, err
			}
			*zstdEnc = enc
		}
		return (*zstdEnc).EncodeAll(data, nil), nil

	case AlgoLZ4:
		buf := make([]byte, lz4.CompressBlockBound(len(data)))
		var ht []int
		if len(data) < 1<<16 {
			if *lz4HT == nil {
				*lz4HT = make([]int, 1<<16)
			}
			ht = *lz4HT
		}
		n, err := lz4.CompressBlock(data, buf, ht)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			n = copy(buf, data)
		}
		return buf[:n], nil

	default:
		return nil, fmt.Errorf("unknown codec: %d", algo)
	}
}

// decompressBlock dispatches to the selected codec.  zstdDec is lazy-allocated
// and reused.
func decompressBlock(compressed []byte, origLen int, algo Algorithm, zstdDec **zstd.Decoder) ([]byte, error) {
	switch algo {
	case AlgoSnappy:
		dst := make([]byte, origLen)
		dec, err := snappy.Decode(dst, compressed)
		if err != nil {
			return nil, err
		}
		return dec, nil

	case AlgoZstd:
		if *zstdDec == nil {
			dec, err := zstd.NewReader(nil)
			if err != nil {
				return nil, err
			}
			*zstdDec = dec
		}
		dst := make([]byte, origLen)
		return (*zstdDec).DecodeAll(compressed, dst[:0])

	case AlgoLZ4:
		dst := make([]byte, origLen)
		n, err := lz4.UncompressBlock(compressed, dst)
		if err != nil {
			return nil, err
		}
		return dst[:n], nil

	default:
		return nil, fmt.Errorf("unknown codec: %d", algo)
	}
}
