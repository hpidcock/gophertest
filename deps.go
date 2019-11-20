package main

import (
	"go/build"
	"path/filepath"
)

type node struct {
	path         string
	pkg          *build.Package
	test         bool
	testPath     string
	dependencies []*node
	dependants   []*node
	obj          string
	workDir      string
	mark         bool
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
	bp, err := buildCtx.Import(path, searchDir, build.FindOnly)
	if err != nil {
		return err
	}
	if bp.Dir != n.pkg.Dir {
		if bp.Goroot {
			return addPackage(path, searchDir, false)
		}
		//log.Printf("%s is ambigious with %s", bp.Dir, n.pkg.Dir)
		return nil
	}
	return nil
}

func addPackage(path string, searchDir string, test bool) error {
	bp, err := buildCtx.Import(path, searchDir, build.ImportComment)
	if err != nil {
		return err
	}
	if !filepath.HasPrefix(bp.Dir, srcDir) {
		searchDir = bp.Dir
	}
	n := &node{
		path: path,
		pkg:  bp,
		test: test && len(bp.TestGoFiles) > 0,
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
		tp := &build.Package{
			Dir:           bp.Dir,
			Name:          bp.Name + "_test",
			ImportPath:    bp.ImportPath + "_test",
			Root:          bp.Root,
			SrcRoot:       bp.SrcRoot,
			PkgRoot:       bp.PkgRoot,
			PkgTargetRoot: bp.PkgTargetRoot,
			TestGoFiles:   bp.XTestGoFiles,
			TestImports:   bp.XTestImports,
			TestImportPos: bp.XTestImportPos,
		}
		nodeMap[path+"_test"] = &node{
			path:     path + "_test",
			pkg:      tp,
			test:     true,
			testPath: path,
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
