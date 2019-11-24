package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/gophertest/build"
	"golang.org/x/sync/errgroup"
)

func buildWave(waveSet map[string]*node) error {
	eg, ctx := errgroup.WithContext(context.Background())
	for _, nn := range waveSet {
		n := nn
		if n.obj != "" {
			continue
		}
		unmetDeps := false
		for _, dep := range n.dependencies {
			if dep.obj == "" {
				unmetDeps = true
				break
			}
		}
		if unmetDeps {
			continue
		}
		eg.Go(func() error {
			return buildNode(ctx, n)
		})
		// err := buildNode(context.Background(), n)
		// if err != nil {
		// 	return err
		// }
	}
	//return nil
	return eg.Wait()
}

func buildNode(ctx context.Context, n *node) error {
	var err error

	n.workDir, err = ioutil.TempDir(workDir, n.pkg.Name)
	if err != nil {
		return err
	}

	if buildCtx.CgoEnabled {
		if n.pkg.Goroot {
			n.obj = n.pkg.PkgObj
			return nil
		}
	}

	if len(n.pkg.CgoFiles) > 0 {
		return fmt.Errorf("cannot build CgoFiles %q", n.path)
	}
	if len(n.pkg.CFiles) > 0 {
		return fmt.Errorf("cannot build CFiles %q", n.path)
	}
	if len(n.pkg.CXXFiles) > 0 {
		return fmt.Errorf("cannot build CXXFiles %q", n.path)
	}
	if len(n.pkg.MFiles) > 0 {
		return fmt.Errorf("cannot build MFiles %q", n.path)
	}
	if len(n.pkg.FFiles) > 0 {
		return fmt.Errorf("cannot build FFiles %q", n.path)
	}
	if len(n.pkg.SwigFiles) > 0 {
		return fmt.Errorf("cannot build SwigFiles %q", n.path)
	}
	if len(n.pkg.SwigCXXFiles) > 0 {
		return fmt.Errorf("cannot build SwigCXXFiles %q", n.path)
	}
	if len(n.pkg.SysoFiles) > 0 {
		return fmt.Errorf("cannot build SysoFiles %q", n.path)
	}

	//log.Printf("building %q...", n.path)
	asm := len(n.pkg.SFiles) > 0
	objFile := path.Join(n.workDir, "obj")
	asmImportFile := path.Join(n.workDir, "go_asm.h")
	symabisFile := path.Join(n.workDir, "symabis")
	importConfigFile := path.Join(n.workDir, "importcfg")

	// GOROOT non-domain packages are considered std lib packages by gc.
	stdLibrary := n.pkg.Goroot && !strings.Contains(strings.Split(n.pkg.ImportPath, "/")[0], ".")
	isRuntime := false
	if stdLibrary {
		switch n.pkg.ImportPath {
		case "runtime", "internal/cpu", "internal/bytealg":
			isRuntime = true
		}
		if strings.HasPrefix(n.pkg.ImportPath, "runtime/internal") {
			isRuntime = true
		}
	}
	isComplete := !asm
	if stdLibrary {
		// From go/src/cmd/go/internal/work/gc.go
		switch n.pkg.ImportPath {
		case "bytes", "internal/poll", "net", "os", "runtime/pprof", "runtime/trace", "sync", "syscall", "time":
			isComplete = false
		}
	}

	asmFiles := []string{}
	for _, f := range n.pkg.SFiles {
		asmFiles = append(asmFiles, path.Base(f))
	}

	if asm {
		err = ioutil.WriteFile(asmImportFile, []byte(""), 0600)
		if err != nil {
			return err
		}
		out := &bytes.Buffer{}
		args := build.AssembleArgs{
			WorkingDirectory: n.pkg.Dir,
			Files:            asmFiles,
			Stdout:           out,
			Stderr:           out,
			TrimPath:         n.workDir + "=>",
			IncludeDirs:      []string{n.workDir, path.Join(pkgDir, "include")},
			Defines: []string{
				"GOOS_" + buildCtx.GOOS,
				"GOARCH_" + buildCtx.GOARCH,
			},
			GenSymABIs: true,
			OutputFile: symabisFile,
		}
		err := build.DefaultTools.Assemble(args)
		if err != nil {
			fmt.Fprint(os.Stderr, out)
			return err
		}
	}

	err = ioutil.WriteFile(importConfigFile, importConfig(n), 0600)
	if err != nil {
		return err
	}

	files := []string{}
	for _, f := range n.pkg.GoFiles {
		files = append(files, path.Base(f))
	}
	if n.test {
		for _, f := range n.pkg.TestGoFiles {
			files = append(files, path.Base(f))
		}
	}
	if len(files) > 0 {
		out := &bytes.Buffer{}
		args := build.CompileArgs{
			WorkingDirectory:         n.pkg.Dir,
			Files:                    files,
			Stdout:                   out,
			Stderr:                   out,
			TrimPath:                 n.workDir + "=>",
			Concurrency:              4,
			PackageImportPath:        n.pkg.ImportPath,
			ImportConfigFile:         importConfigFile,
			CompilingStandardLibrary: stdLibrary,
			CompilingRuntimeLibrary:  isRuntime,
			Complete:                 isComplete,
			Pack:                     true,
			OutputFile:               objFile,
		}
		if asm {
			args.SymABIsFile = symabisFile
			args.AsmHeaderFile = asmImportFile
		}
		err := build.DefaultTools.Compile(args)
		if err != nil {
			fmt.Fprint(os.Stderr, out)
			return err
		}
	}

	if asm {
		asmObjs := []string{}
		for _, asmFile := range asmFiles {
			asmObj := path.Join(n.workDir, strings.TrimSuffix(path.Base(asmFile), ".s")+".o")
			out := &bytes.Buffer{}
			args := build.AssembleArgs{
				WorkingDirectory: n.pkg.Dir,
				Files:            []string{asmFile},
				Stdout:           out,
				Stderr:           out,
				TrimPath:         n.workDir + "=>",
				IncludeDirs:      []string{n.workDir, path.Join(pkgDir, "include")},
				Defines: []string{
					"GOOS_" + buildCtx.GOOS,
					"GOARCH_" + buildCtx.GOARCH,
				},
				OutputFile: asmObj,
			}
			err := build.DefaultTools.Assemble(args)
			if err != nil {
				fmt.Fprint(os.Stderr, out)
				return err
			}
			asmObjs = append(asmObjs, asmObj)
		}

		out := &bytes.Buffer{}
		args := build.PackArgs{
			WorkingDirectory: n.pkg.Dir,
			Stdout:           out,
			Stderr:           out,
			Op:               build.Append,
			ObjectFile:       objFile,
			Names:            asmObjs,
		}
		err := build.DefaultTools.Pack(args)
		if err != nil {
			fmt.Fprint(os.Stderr, out)
			return err
		}
	}

	n.obj = objFile
	return nil
}

func importConfig(n *node) []byte {
	cfg := &bytes.Buffer{}
	fmt.Fprintf(cfg, "# import config\n")
	for _, dep := range n.dependencies {
		if dep.path != dep.pkg.ImportPath {
			fmt.Fprintf(cfg, "importmap %s=%s\n", dep.path, dep.pkg.ImportPath)
		}
	}
	for _, dep := range n.dependencies {
		fmt.Fprintf(cfg, "packagefile %s=%s\n", dep.pkg.ImportPath, dep.obj)
	}
	return cfg.Bytes()
}

func buildAll() error {
	var err error

	wave := make(map[string]*node)
	for _, n := range nodeMap {
		if len(n.dependencies) == 0 {
			wave[n.path] = n
		}
	}

	for len(wave) > 0 {
		err = buildWave(wave)
		if err != nil {
			return err
		}

		nextWave := make(map[string]*node)
		for _, n := range wave {
			for _, d := range n.dependants {
				nextWave[d.path] = d
			}
		}
		wave = nextWave
	}

	return nil
}

func importConfigLink() []byte {
	cfg := &bytes.Buffer{}
	fmt.Fprintf(cfg, "# import config\n")
	for _, dep := range nodeMap {
		if dep.path == "main" {
			continue
		}
		fmt.Fprintf(cfg, "packagefile %s=%s\n", dep.pkg.ImportPath, dep.obj)
	}
	return cfg.Bytes()
}

func link() error {
	mainNode := nodeMap["main"]

	exeDir := path.Join(workDir, "exe")
	err := os.Mkdir(exeDir, 0700)
	if err != nil {
		return err
	}

	importConfigFile := path.Join(exeDir, "importcfg.link")
	err = ioutil.WriteFile(importConfigFile, importConfigLink(), 0600)
	if err != nil {
		return err
	}

	out := &bytes.Buffer{}
	args := build.LinkArgs{
		WorkingDirectory: exeDir,
		Stdout:           out,
		Stderr:           out,
		BuildMode:        "exe",
		ExternalLinker:   "gcc",
		ImportConfigFile: importConfigFile,
		OutputFile:       outFile,
		Files:            []string{mainNode.obj},
	}
	build.DefaultTools.Link(args)
	if err != nil {
		fmt.Fprint(os.Stderr, out)
		return err
	}
	return nil
}
