package boxtest

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// ─── Pure io.Reader / io.Writer streaming tests ────────────────────

func TestStreamingCompressDecompress(t *testing.T) {
	original := make([]byte, 1<<20) // 1 MB
	for i := range original {
		original[i] = byte((i*7 + i/256) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			var compressed bytes.Buffer
			n, err := CompressStream(&compressed, bytes.NewReader(original),
				DefaultBlockSize, cfg.algo, cfg.level)
			if err != nil {
				t.Fatal("compress:", err)
			}
			if n != int64(len(original)) {
				t.Fatalf("n=%d want %d", n, len(original))
			}

			var result bytes.Buffer
			dn, err := DecompressStream(&result, &compressed)
			if err != nil {
				t.Fatal("decompress:", err)
			}
			if dn != int64(len(original)) {
				t.Fatalf("dn=%d want %d", dn, len(original))
			}
			if !bytes.Equal(original, result.Bytes()) {
				t.Fatal("roundtrip mismatch")
			}
		})
	}
}

func TestStreamingRoundtripLarge(t *testing.T) {
	original := make([]byte, 10<<20) // 10 MB
	for i := range original {
		original[i] = byte((i*7 + i/4096) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			var compressed bytes.Buffer
			CompressStream(&compressed, bytes.NewReader(original),
				DefaultBlockSize, cfg.algo, cfg.level)

			var result bytes.Buffer
			DecompressStream(&result, &compressed)
			if !bytes.Equal(original, result.Bytes()) {
				t.Fatal("large roundtrip mismatch")
			}
		})
	}
}

func TestStreamingOutputIsValidFile(t *testing.T) {
	original := make([]byte, 256<<10)
	for i := range original {
		original[i] = byte((i * 13) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			var buf bytes.Buffer
			CompressStream(&buf, bytes.NewReader(original), 16<<10, cfg.algo, cfg.level)

			dst := tmpPath(t, "stream.sc")
			os.WriteFile(dst, buf.Bytes(), 0644)

			cf, err := OpenCompressedFile(dst)
			if err != nil {
				t.Fatal("open:", err)
			}
			defer cf.Close()

			if !cf.CleanClose() {
				t.Fatal("expected clean close")
			}
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch via file open")
			}

			// Random access should work on stream output.
			b := make([]byte, 100)
			cf.ReadAt(b, 10000)
			if !bytes.Equal(original[10000:10100], b) {
				t.Fatal("ReadAt mismatch")
			}
		})
	}
}

func TestStreamCompressorIncremental(t *testing.T) {
	// Incremental writes via StreamCompressor (not CompressStream).
	data := make([]byte, 100000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			var compressed bytes.Buffer
			sc, _ := NewStreamCompressor(&compressed, 4096, cfg.algo, cfg.level)

			// Write in small pieces.
			for i := 0; i < len(data); i += 7 {
				end := i + 7
				if end > len(data) {
					end = len(data)
				}
				sc.Write(data[i:end])
			}
			sc.Close()

			var result bytes.Buffer
			DecompressStream(&result, &compressed)
			if !bytes.Equal(data, result.Bytes()) {
				t.Fatal("incremental roundtrip mismatch")
			}
		})
	}
}
