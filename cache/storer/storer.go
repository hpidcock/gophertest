package storer

import (
	"context"
	"fmt"
	gobuild "go/build"
	"os"
	"path"
	"path/filepath"

	"github.com/hpidcock/gophertest/builder"
	"github.com/hpidcock/gophertest/util"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
)

type Storer struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
}

func (s *Storer) Visit(ctx context.Context, node *dag.Node) error {
	if node.ImportPath == "main" {
		return nil
	}

	rebuilt := false
	for _, meta := range node.Meta {
		switch m := meta.(type) {
		case *builder.BuildMeta:
			rebuilt = m.Rebuilt
		}
	}
	if !rebuilt {
		return nil
	}

	if node.Shlib == "" {
		return fmt.Errorf("missing shlib")
	}

	cacheDir := util.PackageCacheDir(s.CacheDir, node.ImportPath)
	err := os.MkdirAll(cacheDir, 0777)
	if err != nil {
		return err
	}
	lock, err := util.LockDirectory(cacheDir)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	dstFile := path.Join(cacheDir, "cache.obj")
	err = util.FileCopy(node.Shlib, dstFile)
	if err != nil {
		return err
	}

	existingGoFiles, err := filepath.Glob(path.Join(cacheDir, "*.go"))
	if err != nil {
		return err
	}
	for _, v := range existingGoFiles {
		err := os.Remove(v)
		if err != nil {
			return err
		}
	}
	for _, goFile := range node.GoFiles {
		if goFile.Dir == node.SourceDir {
			continue
		}
		err := util.FileCopy(
			path.Join(goFile.Dir, goFile.Filename),
			path.Join(cacheDir, goFile.Filename),
		)
		if err != nil {
			return err
		}
	}

	existingSFiles, err := filepath.Glob(path.Join(cacheDir, "*.s"))
	if err != nil {
		return err
	}
	for _, v := range existingSFiles {
		err := os.Remove(v)
		if err != nil {
			return err
		}
	}
	for _, sFile := range node.SFiles {
		if sFile.Dir == node.SourceDir {
			continue
		}
		err := util.FileCopy(
			path.Join(sFile.Dir, sFile.Filename),
			path.Join(cacheDir, sFile.Filename),
		)
		if err != nil {
			return err
		}
	}

	return nil
}
