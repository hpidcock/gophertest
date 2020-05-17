package builder

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
)

type BuildMeta struct {
	Rebuilt bool
}

type Builder struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	WorkDir string
}

type BuildInfo struct {
	CompilingStandardLibrary bool
	CompilingRuntimeLibrary  bool
	IsComplete               bool
	HasASM                   bool

	BuildID string

	WorkDir          string
	CompileSourceDir string

	ObjFile          string
	ASMImportFile    string
	SymABIsFile      string
	ImportConfigFile string
}

func (b *Builder) Visit(ctx context.Context, node *dag.Node) error {
	var err error

	if node.Shlib != "" {
		node.Meta = append(node.Meta, &BuildMeta{
			Rebuilt: false,
		})
		// Using cached shlib
		return nil
	}

	bi := &BuildInfo{}
	for _, meta := range node.Meta {
		switch m := meta.(type) {
		case *hasher.HashMeta:
			bi.BuildID = m.BuildID
		}
	}
	if bi.BuildID == "" {
		return fmt.Errorf("build id missing")
	}

	bi.WorkDir = path.Join(append([]string{b.WorkDir, "build"}, strings.Split(node.ImportPath, "/")...)...)
	err = os.MkdirAll(bi.WorkDir, 0777)
	if err != nil {
		return err
	}

	bi.CompileSourceDir = node.SourceDir
	hasSourceRewrite := false
	if !hasSourceRewrite {
		for _, v := range node.GoFiles {
			if v.Dir != bi.CompileSourceDir {
				hasSourceRewrite = true
				break
			}
		}
	}
	if !hasSourceRewrite {
		for _, v := range node.SFiles {
			if v.Dir != bi.CompileSourceDir {
				hasSourceRewrite = true
				break
			}
		}
	}

	if hasSourceRewrite {
		for _, v := range node.GoFiles {
			err = os.Symlink(path.Join(v.Dir, v.Filename), path.Join(bi.WorkDir, v.Filename))
			if err != nil {
				return err
			}
		}
		for _, v := range node.SFiles {
			err = os.Symlink(path.Join(v.Dir, v.Filename), path.Join(bi.WorkDir, v.Filename))
			if err != nil {
				return err
			}
		}
		bi.CompileSourceDir = bi.WorkDir
	}

	bi.HasASM = len(node.SFiles) > 0
	bi.ObjFile = path.Join(bi.WorkDir, "obj")
	bi.ASMImportFile = path.Join(bi.WorkDir, "go_asm.h")
	bi.SymABIsFile = path.Join(bi.WorkDir, "symabis")
	bi.ImportConfigFile = path.Join(bi.WorkDir, "importcfg")

	// GOROOT non-domain packages are considered std lib packages by gc.
	bi.CompilingStandardLibrary = node.Goroot && !strings.Contains(strings.Split(node.ImportPath, "/")[0], ".")
	bi.CompilingRuntimeLibrary = false
	if bi.CompilingStandardLibrary {
		switch node.ImportPath {
		case "runtime", "internal/cpu", "internal/bytealg":
			bi.CompilingRuntimeLibrary = true
		}
		if strings.HasPrefix(node.ImportPath, "runtime/internal") {
			bi.CompilingRuntimeLibrary = true
		}
	}
	bi.IsComplete = !bi.HasASM
	if bi.CompilingStandardLibrary {
		// From go/src/cmd/go/internal/work/gc.go
		switch node.ImportPath {
		case "bytes", "internal/poll", "net", "os", "runtime/pprof", "runtime/trace", "sync", "syscall", "time":
			bi.IsComplete = false
		}
	}

	if bi.HasASM {
		err = b.genSymABIs(ctx, node, bi)
		if err != nil {
			return err
		}
	}

	err = b.build(ctx, node, bi)
	if err != nil {
		return err
	}

	if bi.HasASM {
		err = b.asmBuild(ctx, node, bi)
		if err != nil {
			return err
		}
	}

	out := &bytes.Buffer{}
	_, err = b.Tools.BuildID(build.BuildIDArgs{
		Context:          b.BuildCtx,
		WorkingDirectory: bi.CompileSourceDir,
		ObjectFile:       bi.ObjFile,
		Stderr:           out,
		Write:            true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed writing build id: %v", out)
		return err
	}

	node.Shlib = bi.ObjFile
	node.Meta = append(node.Meta, &BuildMeta{
		Rebuilt: true,
	})
	return nil
}

func (b *Builder) genSymABIs(ctx context.Context, node *dag.Node, bi *BuildInfo) error {
	asmFiles := []string{}
	for _, f := range node.SFiles {
		asmFiles = append(asmFiles, f.Filename)
	}
	err := ioutil.WriteFile(bi.ASMImportFile, []byte(""), 0666)
	if err != nil {
		return err
	}
	out := &bytes.Buffer{}
	args := build.AssembleArgs{
		Context:          b.BuildCtx,
		WorkingDirectory: bi.CompileSourceDir,
		Files:            asmFiles,
		Stdout:           out,
		Stderr:           out,
		TrimPath:         bi.WorkDir + "=>",
		IncludeDirs:      []string{bi.WorkDir, path.Join(b.BuildCtx.GOROOT, "pkg", "include")},
		Defines: []string{
			"GOOS_" + b.BuildCtx.GOOS,
			"GOARCH_" + b.BuildCtx.GOARCH,
		},
		GenSymABIs: true,
		OutputFile: bi.SymABIsFile,
	}
	err = b.Tools.Assemble(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed generating sym abis: %v", out)
		return err
	}
	return nil
}

func (b *Builder) asmBuild(ctx context.Context, node *dag.Node, bi *BuildInfo) error {
	asmObjs := []string{}
	for _, asmFile := range node.SFiles {
		asmObj := path.Join(bi.WorkDir, strings.TrimSuffix(asmFile.Filename, ".s")+".o")
		out := &bytes.Buffer{}
		args := build.AssembleArgs{
			Context:          b.BuildCtx,
			WorkingDirectory: bi.CompileSourceDir,
			Files:            []string{asmFile.Filename},
			Stdout:           out,
			Stderr:           out,
			TrimPath:         bi.WorkDir + "=>",
			IncludeDirs:      []string{bi.WorkDir, path.Join(b.BuildCtx.GOROOT, "pkg", "include")},
			Defines: []string{
				"GOOS_" + b.BuildCtx.GOOS,
				"GOARCH_" + b.BuildCtx.GOARCH,
			},
			OutputFile: asmObj,
		}
		err := b.Tools.Assemble(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed assembling: %v", out)
			return err
		}
		asmObjs = append(asmObjs, asmObj)
	}

	out := &bytes.Buffer{}
	args := build.PackArgs{
		Context:          b.BuildCtx,
		WorkingDirectory: bi.CompileSourceDir,
		Stdout:           out,
		Stderr:           out,
		Op:               build.Append,
		ObjectFile:       bi.ObjFile,
		Names:            asmObjs,
	}
	err := b.Tools.Pack(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed packing: %v", out)
		return err
	}
	return nil
}

func (b *Builder) build(ctx context.Context, node *dag.Node, bi *BuildInfo) error {
	err := b.writeImportConfig(ctx, node, bi)
	if err != nil {
		return err
	}

	files := []string{}
	for _, f := range node.GoFiles {
		files = append(files, f.Filename)
		if !node.Tests && f.Test {
			return fmt.Errorf("package %q contains unused tests", node.ImportPath)
		}
	}

	out := &bytes.Buffer{}
	args := build.CompileArgs{
		Context:                  b.BuildCtx,
		WorkingDirectory:         bi.CompileSourceDir,
		Files:                    files,
		Stdout:                   out,
		Stderr:                   out,
		TrimPath:                 bi.WorkDir + "=>",
		Concurrency:              4,
		PackageImportPath:        node.ImportPath,
		ImportConfigFile:         bi.ImportConfigFile,
		CompilingStandardLibrary: bi.CompilingStandardLibrary,
		CompilingRuntimeLibrary:  bi.CompilingRuntimeLibrary,
		Complete:                 bi.IsComplete,
		Pack:                     true,
		OutputFile:               bi.ObjFile,
		BuildID:                  bi.BuildID + "/" + bi.BuildID,
	}
	if bi.HasASM {
		args.SymABIsFile = bi.SymABIsFile
		args.AsmHeaderFile = bi.ASMImportFile
	}
	err = b.Tools.Compile(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed compiling: %v", out)
		return err
	}
	return nil
}

func (b *Builder) writeImportConfig(ctx context.Context, node *dag.Node, bi *BuildInfo) error {
	cfg := &bytes.Buffer{}
	fmt.Fprintf(cfg, "# import config\n")
	for originalPath, rewritePath := range node.ImportMap {
		fmt.Fprintf(cfg, "importmap %s=%s\n", originalPath, rewritePath)
	}
	for _, dep := range node.Imports {
		dep.Mutex.Lock()
		fmt.Fprintf(cfg, "packagefile %s=%s\n", dep.ImportPath, dep.Shlib)
		dep.Mutex.Unlock()
	}
	err := ioutil.WriteFile(bi.ImportConfigFile, cfg.Bytes(), 0666)
	if err != nil {
		return err
	}
	return nil
}
