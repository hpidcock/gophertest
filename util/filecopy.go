package util

import (
	"io"
	"os"
)

// FileCopy with default permissions.
func FileCopy(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	dst, err := os.Create(dstPath)
	if err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	if err != nil {
		src.Close()
		dst.Close()
		return err
	}
	src.Close()
	dst.Close()
	return nil
}
