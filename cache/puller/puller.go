package puller

import (
	"bytes"
	"context"
	"fmt"
	gobuild "go/build"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/cache/hasher"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/util"
	"github.com/pkg/errors"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type Puller struct {
	Logger   Logger
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
	WorkDir  string
}

func (p *Puller) Visit(ctx context.Context, node *dag.Node) error {
	if node.ImportPath == "main" || node.Intrinsic {
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

	manifestFilepath := path.Join(cacheDir, fmt.Sprintf("%s.manifest", node.Name))
	if _, err := os.Stat(manifestFilepath); os.IsNotExist(err) {
		return nil
	}

	manifestBytes, err := ioutil.ReadFile(manifestFilepath)
	if err != nil {
		return errors.WithStack(err)
	}
	manifest := strings.Split(string(manifestBytes), "\n")

	cacheObj := path.Join(cacheDir, fmt.Sprintf("%s.obj", node.Name))
	if _, err := os.Stat(cacheObj); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.WithStack(err)
	}

	out := &bytes.Buffer{}
	readBuildID, err := p.Tools.BuildID(build.BuildIDArgs{
		Context:    p.BuildCtx,
		Stderr:     out,
		ObjectFile: cacheObj,
		Write:      false,
	})
	if err != nil {
		p.Logger.Infof("failed to read build id for %q: %s", node.Name, err.Error())
		return nil
	}
	if !strings.Contains(readBuildID, buildID) {
		return nil
	}

	overwriteGoFiles := map[string]struct{}{}
	for _, filename := range manifest {
		if !strings.HasSuffix(filename, ".go") {
			continue
		}
		filepath := path.Join(cacheDir, filename)
		if _, err := os.Stat(filepath); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return errors.WithMessagef(err, "looking for cached go file %q", filename)
		}
		overwriteGoFiles[filename] = struct{}{}
	}

	overwriteSFiles := map[string]struct{}{}
	for _, filename := range manifest {
		if !strings.HasSuffix(filename, ".s") {
			continue
		}
		filepath := path.Join(cacheDir, filename)
		if _, err := os.Stat(filepath); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return errors.WithMessagef(err, "looking for cached asm file %q", filename)
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

	node.Shlib = cacheObj
	node.GoFiles = replacementGoFiles
	node.SFiles = replacementSFiles

	return nil
}
