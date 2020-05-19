package util

import (
	"io"
	"os"

	"github.com/pkg/errors"
)

// FileCopy with default permissions.
func FileCopy(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return errors.WithStack(err)
	}
	dst, err := os.Create(dstPath)
	if err != nil {
		src.Close()
		return errors.WithStack(err)
	}
	_, err = io.Copy(dst, src)
	if err != nil {
		src.Close()
		dst.Close()
		return errors.WithStack(err)
	}
	src.Close()
	dst.Close()
	return nil
}
