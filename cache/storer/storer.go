package storer

import (
	"context"
	"fmt"
	gobuild "go/build"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/hpidcock/gophertest/builder"
	"github.com/hpidcock/gophertest/util"
	"github.com/pkg/errors"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type Storer struct {
	Logger   Logger
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
}

func (s *Storer) Visit(ctx context.Context, node *dag.Node) (errOut error) {
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
	if _, err := os.Stat(manifestFilepath); os.IsNotExist(err) {
		// Do nothing
	} else if err != nil {
		return errors.WithStack(err)
	} else {
		// Remove old files from previous cache.
		manifestBytes, err := ioutil.ReadFile(manifestFilepath)
		if err != nil {
			return errors.WithStack(err)
		}
		manifest := strings.Split(string(manifestBytes), "\n")
		for _, f := range manifest {
			filepath := path.Join(cacheDir, f)
			err := os.Remove(filepath)
			if os.IsNotExist(err) {
				continue
			} else if err != nil {
				return errors.WithStack(err)
			}
		}
	}

	manifestFile, err := os.Create(manifestFilepath)
	if err != nil {
		return errors.WithStack(err)
	}
	defer func() {
		err := manifestFile.Close()
		if err != nil {
			err = errors.WithStack(err)
			if errOut != nil {
				fmt.Println(err.Error())
			} else {
				errOut = err
			}
			return
		}
	}()

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
