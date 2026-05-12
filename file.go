package boxtest

import (
	"fmt"
	"io"
	"os"
)

// CompressFile reads srcPath and writes a compressed version to dstPath.
func CompressFile(srcPath, dstPath string, blockSize int, algo Algorithm, level int) error {
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	w, err := NewCompressedWriter(dstPath, blockSize, algo, level)
	if err != nil {
		return err
	}
	if len(src) > 0 {
		if _, err := w.Write(src); err != nil {
			w.Close()
			return err
		}
	}
	return w.Close()
}

// DecompressFile fully decompresses srcPath to dstPath.
func DecompressFile(srcPath, dstPath string) error {
	cf, err := OpenCompressedFile(srcPath)
	if err != nil {
		return err
	}
	defer cf.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, cf)
	return err
}
