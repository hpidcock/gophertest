package puller

import (
	"bytes"
	"context"
	"fmt"
	gobuild "go/build"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/cache/hasher"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/util"
	"github.com/pkg/errors"
)

type Puller struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
	WorkDir  string
}

func (p *Puller) Visit(ctx context.Context, node *dag.Node) error {
	if node.ImportPath == "main" {
		return nil
	}

	buildID := ""
	for _, meta := range node.Meta {
		switch m := meta.(type) {
		case *hasher.HashMeta:
			buildID = m.BuildID
		}
	}
	if buildID == "" {
		return fmt.Errorf("missing build id")
	}

	cacheDir := util.PackageCacheDir(p.CacheDir, node.ImportPath)
	cacheObj := path.Join(cacheDir, "cache.obj")
	if _, err := os.Stat(cacheObj); os.IsNotExist(err) {
		return nil
	}

	out := &bytes.Buffer{}
	readBuildID, err := p.Tools.BuildID(build.BuildIDArgs{
		Context:    p.BuildCtx,
		Stderr:     out,
		ObjectFile: cacheObj,
		Write:      false,
	})
	if err != nil {
		return errors.WithStack(err)
	}
	if !strings.Contains(readBuildID, buildID) {
		return nil
	}

	node.Shlib = cacheObj

	goFiles, err := filepath.Glob(path.Join(cacheDir, "*.go"))
	if err != nil {
		return errors.WithStack(err)
	}
	overwriteGoFiles := map[string]struct{}{}
	for _, v := range goFiles {
		filename := path.Base(v)
		if _, err := os.Stat(v); err != nil {
			return errors.WithMessagef(err, "looking for cached go file %q", v)
		}
		overwriteGoFiles[filename] = struct{}{}
	}

	sFiles, err := filepath.Glob(path.Join(cacheDir, "*.s"))
	if err != nil {
		return errors.WithStack(err)
	}
	overwriteSFiles := map[string]struct{}{}
	for _, v := range sFiles {
		filename := path.Base(v)
		if _, err := os.Stat(v); err != nil {
			return errors.WithMessagef(err, "looking for cached asm file %q", v)
		}
		overwriteSFiles[filename] = struct{}{}
	}

	replacementGoFiles := []dag.GoFile(nil)
	for _, v := range node.GoFiles {
		if _, ok := overwriteGoFiles[v.Filename]; !ok {
			replacementGoFiles = append(replacementGoFiles, v)
			continue
		}
		delete(overwriteGoFiles, v.Filename)
		v.Dir = cacheDir
		replacementGoFiles = append(replacementGoFiles, v)
	}
	for k := range overwriteGoFiles {
		goFile := dag.GoFile{
			Dir:      cacheDir,
			Filename: k,
			Test:     strings.HasSuffix(k, "_test.go"),
		}
		replacementGoFiles = append(replacementGoFiles, goFile)
	}
	node.GoFiles = replacementGoFiles

	replacementSFiles := []dag.SFile(nil)
	for _, v := range node.SFiles {
		if _, ok := overwriteSFiles[v.Filename]; !ok {
			replacementSFiles = append(replacementSFiles, v)
			continue
		}
		delete(overwriteSFiles, v.Filename)
		v.Dir = cacheDir
		replacementSFiles = append(replacementSFiles, v)
	}
	for k := range overwriteSFiles {
		goFile := dag.SFile{
			Dir:      cacheDir,
			Filename: k,
		}
		replacementSFiles = append(replacementSFiles, goFile)
	}
	node.SFiles = replacementSFiles

	return nil
}
