package boxtest

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"
)

// Shared test configs used by all test files in this package.
type testConfig struct {
	name  string
	algo  Algorithm
	level int
}

var benchConfigs = []testConfig{
	{"Snappy", AlgoSnappy, 0},
	{"ZstdFastest", AlgoZstd, ZstdFastest},
	{"ZstdDefault", AlgoZstd, ZstdDefault},
	{"ZstdBetter", AlgoZstd, ZstdBetter},
	{"ZstdBest", AlgoZstd, ZstdBest},
	{"LZ4", AlgoLZ4, 0},
}

func tmpPath(t *testing.T, name string) string {
	return filepath.Join(t.TempDir(), name)
}

// simulateCrash closes the underlying file without writing the footer.
func simulateCrash(t *testing.T, w *CompressedWriter) string {
	t.Helper()
	// Flush bufio so data reaches the file before we crash-close.
	if err := w.bw.Flush(); err != nil {
		t.Fatal("flush:", err)
	}
	path := w.file.Name()
	if err := w.file.Close(); err != nil {
		t.Fatal("crash close:", err)
	}
	return path
}

// ─── Single block ──────────────────────────────────────────────────

func TestSingleBlock(t *testing.T) {
	original := make([]byte, 100)
	for i := range original {
		original[i] = byte(i % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, 4096, cfg.algo, cfg.level)
			w.Write(original)
			w.Close()

			cf, _ := OpenCompressedFile(dst)
			defer cf.Close()

			if !cf.CleanClose() {
				t.Fatal("expected clean close")
			}
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch")
			}

			buf := make([]byte, 50)
			cf.ReadAt(buf, 25)
			if !bytes.Equal(original[25:75], buf) {
				t.Fatal("ReadAt mismatch")
			}

			cf.Seek(-10, io.SeekEnd)
			buf2, _ := io.ReadAll(cf)
			if !bytes.Equal(original[90:], buf2) {
				t.Fatal("SeekEnd mismatch")
			}
		})
	}
}

func TestEmptyFile(t *testing.T) {
	dst := tmpPath(t, "dst.sc")
	w, _ := NewCompressedWriter(dst, 4096, AlgoSnappy, 0)
	w.Close()

	cf, _ := OpenCompressedFile(dst)
	defer cf.Close()

	if cf.OriginalSize() != 0 {
		t.Fatalf("size: got %d want 0", cf.OriginalSize())
	}
	if !cf.CleanClose() {
		t.Fatal("expected clean close")
	}
	buf := make([]byte, 10)
	n, err := cf.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("expected EOF, n=%d err=%v", n, err)
	}
}

// ─── Crash recovery ────────────────────────────────────────────────

func TestCrashRecoveryBetweenBlocks(t *testing.T) {
	bs := 4096
	original := make([]byte, bs*5)
	for i := range original {
		original[i] = byte((i * 7) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, bs, cfg.algo, cfg.level)
			w.Write(original)
			path := simulateCrash(t, w)

			cf, err := OpenCompressedFile(path)
			if err != nil {
				t.Fatal("open after crash:", err)
			}
			defer cf.Close()

			if cf.CleanClose() {
				t.Fatal("expected unclean close")
			}
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch after crash")
			}
		})
	}
}

func TestCrashRecoveryMidBlock(t *testing.T) {
	bs := 4096
	full := make([]byte, bs*3)
	partial := make([]byte, 1000)
	for i := range full {
		full[i] = byte((i * 7) % 256)
	}
	for i := range partial {
		partial[i] = byte((i * 13) % 256)
	}

	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, bs, cfg.algo, cfg.level)
			w.Write(append(full, partial...))

			if w.NumBlocks() != 3 {
				t.Fatalf("expected 3 flushed blocks, got %d", w.NumBlocks())
			}
			path := simulateCrash(t, w)

			cf, err := OpenCompressedFile(path)
			if err != nil {
				t.Fatal("open after crash:", err)
			}
			defer cf.Close()

			if cf.OriginalSize() != int64(bs*3) {
				t.Fatalf("size: got %d want %d", cf.OriginalSize(), bs*3)
			}
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(full, result) {
				t.Fatal("partial block should be lost")
			}
		})
	}
}

func TestCrashThenCleanClose(t *testing.T) {
	bs := 4096
	original := make([]byte, bs*3)
	for i := range original {
		original[i] = byte((i * 17) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, bs, cfg.algo, cfg.level)
			w.Write(original)
			w.Close()

			cf, _ := OpenCompressedFile(dst)
			defer cf.Close()

			if !cf.CleanClose() {
				t.Fatal("expected clean close")
			}
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch")
			}
		})
	}
}

// ─── File-level stream writes (file-backed, various chunk sizes) ───

func TestStreamingWriteTinyChunks(t *testing.T) {
	bs := 1024
	original := make([]byte, bs*10)
	for i := range original {
		original[i] = byte((i * 7) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, bs, cfg.algo, cfg.level)
			for _, b := range original {
				w.Write([]byte{b})
			}
			w.Close()

			cf, _ := OpenCompressedFile(dst)
			defer cf.Close()
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch")
			}
		})
	}
}

func TestStreamingWriteRandomChunks(t *testing.T) {
	dataSize := 5 << 20
	original := make([]byte, dataSize)
	for i := range original {
		original[i] = byte((i * 13) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, DefaultBlockSize, cfg.algo, cfg.level)
			rng := newRand(99)
			pos := 0
			for pos < dataSize {
				chunk := 1 + rng.Intn(4096)
				if pos+chunk > dataSize {
					chunk = dataSize - pos
				}
				w.Write(original[pos : pos+chunk])
				pos += chunk
			}
			w.Close()

			cf, _ := OpenCompressedFile(dst)
			defer cf.Close()
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch")
			}
		})
	}
}

func TestStreamingWriteExactBlocks(t *testing.T) {
	bs := 4096
	original := make([]byte, bs*3)
	for i := range original {
		original[i] = byte((i * 17) % 256)
	}
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			dst := tmpPath(t, "dst.sc")
			w, _ := NewCompressedWriter(dst, bs, cfg.algo, cfg.level)
			for i := 0; i < 3; i++ {
				w.Write(original[i*bs : (i+1)*bs])
			}
			w.Close()

			cf, _ := OpenCompressedFile(dst)
			defer cf.Close()
			result, _ := io.ReadAll(cf)
			if !bytes.Equal(original, result) {
				t.Fatal("content mismatch")
			}
		})
	}
}

// deterministic rand (avoid importing math/rand in every test file).
func newRand(seed int64) *prng { return &prng{s: seed} }

type prng struct{ s int64 }

func (r *prng) Intn(n int) int {
	r.s = (r.s*1103515245 + 12345) & 0x7fffffff
	return int(r.s) % n
}
func (r *prng) Int63n(n int64) int64 {
	r.s = (r.s*1103515245 + 12345) & 0x7fffffff
	return r.s % n
}
