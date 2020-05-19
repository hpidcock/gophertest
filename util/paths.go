package util

import (
	"go/build"
	"os"
	"path"

	"github.com/nightlyone/lockfile"
	"github.com/pkg/errors"
)

func PackageCacheDir(cacheDir string, importPath string) string {
	return path.Join(cacheDir, importPath)
}

func LockDirectory(dir string) (lockfile.Lockfile, error) {
	lockFile, err := lockfile.New(path.Join(dir, ".lock"))
	if err != nil {
		return lockFile, errors.WithStack(err)
	}
	return lockFile, lockFile.TryLock()
}

func CacheDir(buildCtx build.Context) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", errors.WithStack(err)
	}
	dir := path.Join(cacheDir, buildCtx.GOOS+"_"+buildCtx.GOARCH)
	err = os.MkdirAll(dir, 0777)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return dir, nil
}
