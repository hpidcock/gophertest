package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/hpidcock/gophertest/runner"
)

type testPackage struct {
	path  string
	test  *node
	xtest *node

	testMain string

	benchmarks []benchmark
	tests      []test
}

type benchmark struct {
	path string
	name string
}

type test struct {
	path string
	name string
}

func findTests(n *node) error {
	pkg := testPackages[n.testPath]
	if pkg == nil {
		pkg = &testPackage{
			path: n.testPath,
		}
	}

	if n.testPath == n.path {
		pkg.test = n
	} else if n.testPath == strings.TrimSuffix(n.path, "_test") {
		pkg.xtest = n
	} else {
		return fmt.Errorf("invalid test package %q", n.path)
	}

	ts := &token.FileSet{}
	for _, testFile := range n.pkg.TestGoFiles {
		filename := path.Join(n.pkg.Dir, testFile)
		tree, err := parser.ParseFile(ts, filename, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range tree.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok == false {
				continue
			}
			switch {
			case isTest(fd):
				pkg.tests = append(pkg.tests, test{n.path, fd.Name.String()})
			case isBenchmark(fd):
				pkg.benchmarks = append(pkg.benchmarks, benchmark{n.path, fd.Name.String()})
			case isTestMain(fd):
				if pkg.testMain != "" {
					return fmt.Errorf("ambigious TestMain in %q", n.path)
				}
				pkg.testMain = n.path
			}
		}
	}

	if len(pkg.tests) > 0 || len(pkg.benchmarks) > 0 {
		testPackages[n.testPath] = pkg
	}

	return nil
}

func generateMain() error {
	for _, node := range nodeMap {
		if !node.test {
			continue
		}
		err := findTests(node)
		if err != nil {
			return err
		}
	}

	id := -1
	nextID := func() string {
		id++
		return fmt.Sprintf("pkg%d", id)
	}
	ctx := runner.Context{}
	for _, pkg := range testPackages {
		t := runner.Target{
			ImportPath: pkg.path,
			TestName:   nextID(),
			XTestName:  nextID(),
		}
		if pkg.test != nil {
			t.Directory = pkg.test.pkg.Dir
		} else if pkg.xtest != nil {
			t.Directory = pkg.xtest.pkg.Dir
		}
		switch {
		case pkg.test != nil && pkg.test.path == pkg.testMain:
			t.ImportTest = true
			t.Main = t.TestName + ".TestMain"
		case pkg.xtest != nil && pkg.xtest.path == pkg.testMain:
			t.ImportXTest = true
			t.Main = t.XTestName + ".TestMain"
		default:
			t.Main = "defaultMain"
		}
		for _, testFunc := range pkg.tests {
			switch {
			case pkg.test != nil && pkg.test.path == testFunc.path:
				t.ImportTest = true
				t.Tests = append(t.Tests, runner.Test{
					Package: t.TestName,
					Name:    testFunc.name,
				})
			case pkg.xtest != nil && pkg.xtest.path == testFunc.path:
				t.ImportXTest = true
				t.Tests = append(t.Tests, runner.Test{
					Package: t.XTestName,
					Name:    testFunc.name,
				})
			}
		}
		for _, benchmarkFunc := range pkg.benchmarks {
			switch {
			case pkg.test != nil && pkg.test.path == benchmarkFunc.path:
				t.ImportTest = true
				t.Benchmarks = append(t.Benchmarks, runner.Test{
					Package: t.TestName,
					Name:    benchmarkFunc.name,
				})
			case pkg.xtest != nil && pkg.xtest.path == benchmarkFunc.path:
				t.ImportXTest = true
				t.Benchmarks = append(t.Benchmarks, runner.Test{
					Package: t.XTestName,
					Name:    benchmarkFunc.name,
				})
			}
		}
		if t.ImportTest == false && pkg.test != nil {
			t.ImportTest = true
			t.TestName = "_"
		}
		if t.ImportXTest == false && pkg.xtest != nil {
			t.ImportXTest = true
			t.XTestName = "_"
		}
		ctx.Targets = append(ctx.Targets, t)
	}

	sort.Slice(ctx.Targets, func(i, j int) bool {
		return ctx.Targets[i].ImportPath < ctx.Targets[j].ImportPath
	})

	b := &bytes.Buffer{}
	err := runner.Template.Execute(b, ctx)
	if err != nil {
		return err
	}

	srcDir := path.Join(workDir, "main")
	err = os.Mkdir(srcDir, 0700)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(path.Join(srcDir, "main.go"), b.Bytes(), 0664)
	if err != nil {
		return err
	}

	rawImports := []string{}
	for _, pkg := range testPackages {
		if pkg.test != nil {
			rawImports = append(rawImports, pkg.test.path)
		}
		if pkg.xtest != nil {
			rawImports = append(rawImports, pkg.xtest.path)
		}
	}
	rawImports = append(rawImports, runner.Deps...)

	nodeMap["main"] = &node{
		path: "main",
		pkg: &build.Package{
			Dir:        srcDir,
			Name:       "main",
			ImportPath: "main",
			GoFiles:    []string{"main.go"},
			Imports:    rawImports,
		},
	}

	for _, dep := range runner.Deps {
		err = add(dep, srcDir, false)
		if err != nil {
			return err
		}
	}

	return nil
}
