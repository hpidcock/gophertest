package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/hpidcock/gophertest/deferredinit"
	"github.com/hpidcock/gophertest/packages"
	"github.com/hpidcock/gophertest/runner"
)

type testPackage struct {
	path  string
	dir   string
	name  string
	test  *node
	xtest *node

	testMain string
	hasInit  bool
	hasXInit bool

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
			dir:  n.originalDir,
			name: strings.TrimSuffix(n.pkg.Name, "_test"),
		}
	}

	isXTest := false
	if n.testPath == n.path {
		pkg.test = n
	} else if n.testPath == strings.TrimSuffix(n.path, "_test") {
		pkg.xtest = n
		isXTest = true
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
			case isGopherTestInit(fd) && isXTest:
				pkg.hasXInit = true
			case isGopherTestInit(fd) && !isXTest:
				pkg.hasInit = true
			}
		}
	}

	testPackages[n.testPath] = pkg
	return nil
}

type patchedPackage struct {
	path  string
	test  *node
	xtest *node
}

func patchTests() error {
	testImportPaths := []string{}
	pkgs := map[string]*patchedPackage{}
	for _, node := range nodeMap {
		if !node.test {
			continue
		}
		pkg := pkgs[node.testPath]
		if pkg == nil {
			pkg = &patchedPackage{
				path: node.testPath,
			}
			pkgs[node.testPath] = pkg
			testImportPaths = append(testImportPaths, node.testPath)
		}
		if node.isXTest {
			pkg.xtest = node
		} else {
			pkg.test = node
		}
	}

	parsedPackages, err := deferredinit.LoadPackages(srcDir, testImportPaths)
	if err != nil {
		return err
	}

	sem := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		sem <- struct{}{}
	}

	eg, ctx := errgroup.WithContext(context.Background())
	for _, p := range pkgs {
		pkg := p
		eg.Go(func() error {
			select {
			case <-sem:
			case <-ctx.Done():
				return ctx.Err()
			}
			defer func() {
				sem <- struct{}{}
			}()

			rewriteDir := filepath.Join(workDir, "rewrites", pkg.path)
			err := os.MkdirAll(rewriteDir, 0777)
			if err != nil {
				return err
			}
			dir := ""
			if pkg.test != nil {
				dir = pkg.test.originalDir
			} else if pkg.xtest != nil {
				dir = pkg.xtest.originalDir
			}
			rewrittenPkgs, err := deferredinit.RewriteTestPackages(dir, parsedPackages[pkg.path], rewriteDir)
			if err != nil {
				return err
			}
			if len(rewrittenPkgs) > 0 {
				if pkg.test != nil {
					pkg.test.pkg.Dir = rewriteDir
				}
				if pkg.xtest != nil {
					pkg.xtest.pkg.Dir = rewriteDir
				}
				for _, v := range rewrittenPkgs {
					if pkg.test != nil && v.Path == pkg.test.path {
						pkg.test.pkg.TestImports = v.TestImports
						pkg.test.pkg.TestGoFiles = append(pkg.test.pkg.TestGoFiles, v.AddedTestFiles...)
						pkg.test.testComplexity = v.Complexity
					}
					if pkg.xtest != nil && v.Path == pkg.xtest.path {
						pkg.xtest.pkg.TestImports = v.TestImports
						pkg.xtest.pkg.TestGoFiles = append(pkg.xtest.pkg.TestGoFiles, v.AddedTestFiles...)
						pkg.xtest.testComplexity = v.Complexity
					}
				}
			}
			return nil
		})
	}

	err = eg.Wait()
	if err != nil {
		return err
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
		if len(pkg.tests) == 0 && len(pkg.benchmarks) == 0 {
			continue
		}
		t := runner.Target{
			ImportPath: pkg.path,
			Name:       pkg.name,
			TestName:   nextID(),
			XTestName:  nextID(),
			Directory:  pkg.dir,
		}
		if pkg.test != nil {
			t.TestComplexity += pkg.test.testComplexity
		}
		if pkg.xtest != nil {
			t.TestComplexity += pkg.xtest.testComplexity
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

		t.InitFunc = "func(){}"
		t.XInitFunc = "func(){}"
		if pkg.test != nil && pkg.hasInit {
			t.ImportTest = true
			t.InitFunc = t.TestName + ".GopherTestInit"
		}
		if pkg.xtest != nil && pkg.hasXInit {
			t.ImportXTest = true
			t.XInitFunc = t.XTestName + ".GopherTestInit"
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
	err = os.Mkdir(srcDir, 0777)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(path.Join(srcDir, "main.go"), b.Bytes(), 0666)
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
		pkg: &packages.Package{
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
