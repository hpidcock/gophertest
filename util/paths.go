package util

import (
	"path"

	"github.com/nightlyone/lockfile"
)

func PackageCacheDir(cacheDir string, importPath string) string {
	return path.Join(cacheDir, importPath)
}

func LockDirectory(dir string) (lockfile.Lockfile, error) {
	lockFile, err := lockfile.New(path.Join(dir, ".lock"))
	if err != nil {
		return lockFile, err
	}
	return lockFile, lockFile.TryLock()
}
