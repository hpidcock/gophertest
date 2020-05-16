package hasher

import (
	"context"
	"crypto/sha256"
	"fmt"
	gobuild "go/build"
	"io"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/gophertest/build"

	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/version"
)

type HasherMeta struct {
	BuildID string
}

type Hasher struct {
	BuildCtx gobuild.Context
	Tools    build.Tools
}

func (c *Hasher) Visit(ctx context.Context, node *dag.Node) error {
	var provenance []string
	s := sha256.New()

	goCompilerVersion, err := c.Tools.Version()
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(s, "%s:%s:%s:%s:%s:%s:%s:%s:%s:%s:%s:%t",
		version.String,
		runtime.Version(),
		goCompilerVersion,
		c.BuildCtx.Compiler,
		c.BuildCtx.GOARCH,
		c.BuildCtx.GOOS,
		c.BuildCtx.GOPATH,
		c.BuildCtx.GOROOT,
		c.BuildCtx.InstallSuffix,
		strings.Join(c.BuildCtx.ReleaseTags, ":"),
		strings.Join(c.BuildCtx.BuildTags, ":"),
		c.BuildCtx.CgoEnabled)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(s, "%s:%s:%s:%s:%t:%t:%t",
		node.ImportPath,
		node.Name,
		node.SourceDir,
		node.RootDir,
		node.Goroot,
		node.Standard,
		node.Tests)
	if err != nil {
		return err
	}
	provenance = append(provenance, hashToString(s.Sum(nil)))

	for _, imported := range node.Imports {
		imported.Mutex.Lock()
		if len(imported.Meta) != 1 {
			imported.Mutex.Unlock()
			return fmt.Errorf("%v has more than one meta %#v", node.ImportPath, node.Meta)
		}
		for _, meta := range imported.Meta {
			switch m := meta.(type) {
			case *HasherMeta:
				provenance = append(provenance, m.BuildID)
			}
		}
		imported.Mutex.Unlock()
	}

	for _, goFile := range node.GoFiles {
		s := sha256.New()
		_, err := fmt.Fprintf(s, "%s:%s:%t\n", goFile.Dir, goFile.Filename, goFile.Test)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path.Join(goFile.Dir, goFile.Filename), os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		_, err = io.Copy(s, f)
		if err != nil {
			return err
		}
		err = f.Close()
		if err != nil {
			return err
		}
		provenance = append(provenance, hashToString(s.Sum(nil)))
	}

	for _, sFile := range node.SFiles {
		s := sha256.New()
		_, err := fmt.Fprintf(s, "%s:%s\n", sFile.Dir, sFile.Filename)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path.Join(sFile.Dir, sFile.Filename), os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		_, err = io.Copy(s, f)
		if err != nil {
			return err
		}
		err = f.Close()
		if err != nil {
			return err
		}
		provenance = append(provenance, hashToString(s.Sum(nil)))
	}

	sort.Strings(provenance)

	s = sha256.New()
	for _, p := range provenance {
		_, err := fmt.Fprintln(s, p)
		if err != nil {
			return err
		}
	}

	cache := &HasherMeta{
		BuildID: hashToString(s.Sum(nil)),
	}
	node.Meta = append(node.Meta, cache)

	return nil
}
