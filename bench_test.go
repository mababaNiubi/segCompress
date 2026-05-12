package boxtest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type benchResult struct {
	name           string
	originalSize   int64
	compressedSize int64
	ratio          float64
	compressTime   time.Duration
	compressMBps   float64
	avgSeekReadNs  int64
	avgSeekReadUs  float64
}

func TestCompressionComparison(t *testing.T) {
	srcPath := "bigFile.tsb"
	original, err := os.ReadFile(srcPath)
	if err != nil {
		t.Logf("bigFile.tsb not found (%v), generating 50 MB test data", err)
		original = generateTestData(50 << 20)
		t.Logf("Using %d MB random data", len(original)>>20)
	} else {
		t.Logf("Loaded bigFile.tsb: %d bytes (%.1f MB)", len(original), float64(len(original))/(1<<20))
	}

	blockSize := DefaultBlockSize
	seekCount := 2000
	readSize := 4096

	var results []benchResult

	for _, cfg := range benchConfigs {
		t.Logf("Testing %s...", cfg.name)
		dst := tmpPath(t, "dst.sc")

		start := time.Now()
		w, _ := NewCompressedWriter(dst, blockSize, cfg.algo, cfg.level)
		w.Write(original)
		w.Close()
		compressTime := time.Since(start)

		fi, _ := os.Stat(dst)

		cf, _ := OpenCompressedFile(dst)

		// Spot-check correctness.
		for _, pos := range []int64{0, cf.OriginalSize() / 2, cf.OriginalSize() - 100} {
			buf := make([]byte, 100)
			n, _ := cf.ReadAt(buf, pos)
			if n > 0 && !bytes.Equal(original[pos:pos+int64(n)], buf[:n]) {
				t.Fatalf("%s verify at %d: mismatch", cfg.name, pos)
			}
		}

		// Random Seek + Read.
		rng := newRand(42)
		var totalNs int64
		maxOff := cf.OriginalSize() - int64(readSize)
		for i := 0; i < seekCount; i++ {
			offset := rng.Int63n(maxOff)
			buf := make([]byte, readSize)
			t0 := time.Now()
			cf.Seek(offset, io.SeekStart)
			io.ReadFull(cf, buf)
			totalNs += time.Since(t0).Nanoseconds()
		}
		cf.Close()

		origSize := int64(len(original))
		results = append(results, benchResult{
			name:           cfg.name,
			originalSize:   origSize,
			compressedSize: fi.Size(),
			ratio:          float64(fi.Size()) / float64(origSize) * 100,
			compressTime:   compressTime,
			compressMBps:   float64(origSize) / compressTime.Seconds() / (1 << 20),
			avgSeekReadNs:  totalNs / int64(seekCount),
			avgSeekReadUs:  float64(totalNs/int64(seekCount)) / 1000.0,
		})
	}

	printTable(results)
}

func generateTestData(size int) []byte {
	rng := newRand(12345)
	data := make([]byte, size)
	for i := 0; i < size; {
		run := rng.Intn(256) + 16
		val := byte(rng.Intn(256))
		for j := 0; j < run && i < size; j++ {
			data[i] = val
			i++
			if j%8 == 0 {
				val = byte(rng.Intn(256))
			}
		}
	}
	return data
}

func printTable(results []benchResult) {
	sep := strings.Repeat("-", 86)
	fmt.Println("\n" + strings.Repeat("=", 86))
	fmt.Println("  Block Compressor Benchmark  |  Block: 64 KB  |  Seek + Read 4 KB")
	fmt.Println(strings.Repeat("=", 86))
	fmt.Println(sep)
	fmt.Printf("  %-18s | %7s | %12s | %12s\n", "Algorithm", "Ratio%", "Comp MB/s", "Seek+Read(us)")
	fmt.Println(sep)
	for _, r := range results {
		fmt.Printf("  %-18s | %6.1f%% | %10.1f MB/s | %12.2f us\n",
			r.name, r.ratio, r.compressMBps, r.avgSeekReadUs)
	}
	fmt.Println(sep)
	fmt.Println("  Ratio% = compressed/original x 100 (lower=better)")
	fmt.Println(strings.Repeat("=", 86) + "\n")
}
