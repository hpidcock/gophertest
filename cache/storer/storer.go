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
	"github.com/pkg/errors"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
)

type Storer struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
}

func (s *Storer) Cleanup(ctx context.Context, node *dag.Node) error {
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
	err := os.RemoveAll(cacheDir)
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (s *Storer) Update(ctx context.Context, node *dag.Node) error {
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
		return errors.WithStack(err)
	}

	manifestFilepath := path.Join(cacheDir, fmt.Sprintf("%s.manifest", node.Name))
	manifestFile, err := os.Create(manifestFilepath)
	if err != nil {
		return errors.WithStack(err)
	}
	defer manifestFile.Close()

	objFilename := fmt.Sprintf("%s.obj", node.Name)
	_, err = fmt.Fprintln(manifestFile, objFilename)
	if err != nil {
		return errors.WithStack(err)
	}
	objFilepath := path.Join(cacheDir, objFilename)
	err = util.FileCopy(node.Shlib, objFilepath)
	if err != nil {
		return errors.WithStack(err)
	}

	existingGoFiles, err := filepath.Glob(path.Join(cacheDir, "*.go"))
	if err != nil {
		return errors.WithStack(err)
	}
	for _, v := range existingGoFiles {
		err := os.Remove(v)
		if err != nil {
			return errors.WithStack(err)
		}
	}
	for _, goFile := range node.GoFiles {
		if goFile.Dir == node.SourceDir {
			continue
		}
		_, err := fmt.Fprintln(manifestFile, goFile.Filename)
		if err != nil {
			return errors.WithStack(err)
		}
		err = util.FileCopy(
			path.Join(goFile.Dir, goFile.Filename),
			path.Join(cacheDir, goFile.Filename),
		)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	existingSFiles, err := filepath.Glob(path.Join(cacheDir, "*.s"))
	if err != nil {
		return errors.WithStack(err)
	}
	for _, v := range existingSFiles {
		err := os.Remove(v)
		if err != nil {
			return errors.WithStack(err)
		}
	}
	for _, sFile := range node.SFiles {
		if sFile.Dir == node.SourceDir {
			continue
		}
		_, err := fmt.Fprintln(manifestFile, sFile.Filename)
		if err != nil {
			return errors.WithStack(err)
		}
		err = util.FileCopy(
			path.Join(sFile.Dir, sFile.Filename),
			path.Join(cacheDir, sFile.Filename),
		)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}
