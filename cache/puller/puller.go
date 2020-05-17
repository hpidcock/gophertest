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
	lock, err := util.LockDirectory(cacheDir)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	out := &bytes.Buffer{}
	readBuildID, err := p.Tools.BuildID(build.BuildIDArgs{
		Context:    p.BuildCtx,
		Stderr:     out,
		ObjectFile: cacheObj,
		Write:      false,
	})
	if err != nil {
		return err
	}
	if !strings.Contains(readBuildID, buildID) {
		return nil
	}

	workCache := path.Join(p.WorkDir, "cache", node.ImportPath)
	err = os.MkdirAll(workCache, 0777)
	if err != nil {
		return err
	}

	cacheObjCopy := path.Join(workCache, "cache.obj")
	err = util.FileCopy(cacheObj, cacheObjCopy)
	if err != nil {
		return err
	}
	node.Shlib = cacheObjCopy

	goFiles, err := filepath.Glob(path.Join(cacheDir, "*.go"))
	if err != nil {
		return err
	}
	overwriteGoFiles := map[string]struct{}{}
	for _, v := range goFiles {
		filename := path.Base(v)
		err := util.FileCopy(v, path.Join(workCache, v))
		if err != nil {
			return err
		}
		overwriteGoFiles[filename] = struct{}{}
	}

	sFiles, err := filepath.Glob(path.Join(cacheDir, "*.s"))
	if err != nil {
		return err
	}
	overwriteSFiles := map[string]struct{}{}
	for _, v := range sFiles {
		filename := path.Base(v)
		err := util.FileCopy(v, path.Join(workCache, v))
		if err != nil {
			return err
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
		v.Dir = workCache
		replacementGoFiles = append(replacementGoFiles, v)
	}
	for k := range overwriteGoFiles {
		goFile := dag.GoFile{
			Dir:      workCache,
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
		v.Dir = workCache
		replacementSFiles = append(replacementSFiles, v)
	}
	for k := range overwriteSFiles {
		goFile := dag.SFile{
			Dir:      workCache,
			Filename: k,
		}
		replacementSFiles = append(replacementSFiles, goFile)
	}
	node.SFiles = replacementSFiles

	return nil
}
