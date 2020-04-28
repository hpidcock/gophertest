package main

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/hpidcock/gophertest/buildctx"
)

type node struct {
	path           string
	originalDir    string
	pkg            *buildctx.Package
	test           bool
	testPath       string
	dependencies   []*node
	dependants     []*node
	obj            string
	workDir        string
	mark           bool
	isXTest        bool
	testComplexity int
}

func add(path string, searchDir string, test bool) error {
	if path == "C" || path == "unsafe" {
		return nil
	}
	n := nodeMap[path]
	if n == nil || (test && !n.test) {
		return addPackage(path, searchDir, test)
	}
	if n.test {
		return nil
	}
	bp, ok := pkgMap[path]
	if !ok {
		return fmt.Errorf("missing import %q", path)
	}
	if bp.Dir != n.pkg.Dir {
		if bp.Goroot {
			return addPackage(path, searchDir, false)
		}
		log.Printf("%s is ambigious with %s", bp.Dir, n.pkg.Dir)
		return nil
	}
	return nil
}

func addPackage(path string, searchDir string, test bool) error {
	bp, ok := pkgMap[path]
	if !ok {
		return fmt.Errorf("missing import %q", path)
	}
	if !filepath.HasPrefix(bp.Dir, srcDir) {
		searchDir = bp.Dir
	}

	n := &node{
		path:        path,
		originalDir: bp.Dir,
		pkg:         bp,
		test:        test && len(bp.TestGoFiles) > 0,
	}
	if test {
		n.testPath = path
	}
	nodeMap[path] = n
	for _, dep := range bp.Imports {
		err := add(dep, searchDir, false)
		if err != nil {
			return err
		}
	}
	if test && len(bp.TestGoFiles) > 0 {
		for _, dep := range bp.TestImports {
			err := add(dep, searchDir, false)
			if err != nil {
				return err
			}
		}
	}
	if test && len(bp.XTestGoFiles) > 0 {
		tp := *bp
		tp.Name = bp.Name + "_test"
		tp.ImportPath = bp.ImportPath + "_test"
		tp.TestGoFiles = bp.XTestGoFiles
		tp.TestImports = bp.XTestImports
		tp.CFiles = nil
		tp.CXXFiles = nil
		tp.CgoCFLAGS = nil
		tp.CgoCPPFLAGS = nil
		tp.CgoFFLAGS = nil
		tp.CgoFiles = nil
		tp.CgoLDFLAGS = nil
		tp.CgoPkgConfig = nil
		tp.CompiledGoFiles = nil
		tp.FFiles = nil
		tp.GoFiles = nil
		tp.HFiles = nil
		tp.IgnoredGoFiles = nil
		tp.ImportMap = nil
		tp.Imports = nil
		tp.MFiles = nil
		tp.SFiles = nil
		tp.Shlib = ""
		tp.SwigCXXFiles = nil
		tp.SwigFiles = nil
		tp.SysoFiles = nil
		tp.XTestGoFiles = nil
		tp.XTestImports = nil
		nodeMap[path+"_test"] = &node{
			path:        path + "_test",
			originalDir: bp.Dir,
			pkg:         &tp,
			test:        true,
			testPath:    path,
			isXTest:     true,
		}
		for _, dep := range tp.TestImports {
			err := add(dep, searchDir, false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func linkDeps() {
	for _, n := range nodeMap {
		dependencies := []string{}
		dependencies = append(dependencies, n.pkg.Imports...)
		if n.test {
			dependencies = append(dependencies, n.pkg.TestImports...)
		}
		for _, dep := range dependencies {
			if dep == "C" || dep == "unsafe" {
				continue
			}
			other := nodeMap[dep]
			n.dependencies = append(n.dependencies, other)
			other.dependants = append(other.dependants, n)
		}
	}
}
