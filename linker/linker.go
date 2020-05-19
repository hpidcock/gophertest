package linker

import (
	"bytes"
	"context"
	"fmt"
	gobuild "go/build"
	"io/ioutil"
	"os"
	"path"
	"sync"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
	"github.com/pkg/errors"
)

type Linker struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	WorkDir string
	OutFile string

	packageMapMutex sync.Mutex
	packageMap      map[string]string
}

func (l *Linker) Visit(ctx context.Context, node *dag.Node) error {
	if node.ImportPath != "main" {
		if len(node.Deps) == 0 {
			fmt.Printf("warn: node without dependents %q\n", node.ImportPath)
			return nil
		}
		if node.Intrinsic {
			return nil
		}
		if node.Shlib == "" {
			return fmt.Errorf("missing shlib for %q", node.ImportPath)
		}
		if _, err := os.Stat(node.Shlib); os.IsNotExist(err) {
			return fmt.Errorf("missing shlib for %q", node.ImportPath)
		} else if err != nil {
			return errors.WithStack(err)
		}
		l.packageMapMutex.Lock()
		defer l.packageMapMutex.Unlock()
		if l.packageMap == nil {
			l.packageMap = make(map[string]string)
		}
		if _, ok := l.packageMap[node.ImportPath]; ok {
			return fmt.Errorf("package map already contains import %q", node.ImportPath)
		}
		l.packageMap[node.ImportPath] = node.Shlib
		return nil
	}

	if len(node.Deps) > 0 {
		return fmt.Errorf("main has dependants")
	}

	l.packageMapMutex.Lock()
	defer l.packageMapMutex.Unlock()
	if l.packageMap == nil {
		l.packageMap = make(map[string]string)
	}

	exeDir := path.Join(l.WorkDir, "exe")
	err := os.Mkdir(exeDir, 0777)
	if err != nil {
		return errors.WithStack(err)
	}

	importConfigFile := path.Join(exeDir, "importcfg.link")
	err = ioutil.WriteFile(importConfigFile, l.importConfigLink(), 0666)
	if err != nil {
		return errors.WithStack(err)
	}

	out := &bytes.Buffer{}
	args := build.LinkArgs{
		WorkingDirectory: exeDir,
		Stdout:           out,
		Stderr:           out,
		BuildMode:        "exe",
		ExternalLinker:   "gcc",
		ImportConfigFile: importConfigFile,
		OutputFile:       l.OutFile,
		Files:            []string{node.Shlib},
	}
	err = l.Tools.Link(args)
	if err != nil {
		fmt.Fprint(os.Stderr, out)
		return errors.WithStack(err)
	}
	return nil
}

func (l *Linker) importConfigLink() []byte {
	cfg := &bytes.Buffer{}
	fmt.Fprintf(cfg, "# import config\n")
	for importPath, shLib := range l.packageMap {
		if importPath == "main" {
			continue
		}
		fmt.Fprintf(cfg, "packagefile %s=%s\n", importPath, shLib)
	}
	return cfg.Bytes()
}
