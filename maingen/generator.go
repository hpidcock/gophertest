package maingen

import (
	"context"
	"fmt"
	"go/ast"
	gobuild "go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/hpidcock/gophertest/cache/hasher"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/maingen/runner"
	"github.com/hpidcock/gophertest/packages"
	"github.com/pkg/errors"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type Generator struct {
	Logger   Logger
	BuildCtx gobuild.Context
	Tools    build.Tools

	WorkDir string

	testPackagesMutex sync.Mutex
	testPackages      map[string]*testPackage
}

func (g *Generator) FindTests(ctx context.Context, node *dag.Node) error {
	if !node.Tests {
		return nil
	}

	g.testPackagesMutex.Lock()
	defer g.testPackagesMutex.Unlock()
	if g.testPackages == nil {
		g.testPackages = make(map[string]*testPackage)
	}

	testPath := strings.TrimSuffix(node.ImportPath, "_test")

	pkg := g.testPackages[testPath]
	if pkg == nil {
		pkg = &testPackage{
			ImportPath: testPath,
			Dir:        node.SourceDir,
			Name:       strings.TrimSuffix(node.Name, "_test"),
		}
	}

	isXTest := false
	if testPath == node.ImportPath {
		pkg.Test = node
	} else if testPath == strings.TrimSuffix(node.ImportPath, "_test") {
		pkg.XTest = node
		isXTest = true
	} else {
		return fmt.Errorf("invalid test package %q", node.ImportPath)
	}

	ts := &token.FileSet{}
	for _, testFile := range node.GoFiles {
		if !testFile.Test {
			continue
		}
		filename := path.Join(testFile.Dir, testFile.Filename)
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
				pkg.Tests = append(pkg.Tests, test{node.ImportPath, fd.Name.String()})
			case isBenchmark(fd):
				pkg.Benchmarks = append(pkg.Benchmarks, benchmark{node.ImportPath, fd.Name.String()})
			case isTestMain(fd):
				if pkg.TestMain != "" {
					return fmt.Errorf("ambigious TestMain in %q", node.ImportPath)
				}
				pkg.TestMain = node.ImportPath
			case isGopherTestInit(fd) && isXTest:
				pkg.HasXInit = true
			case isGopherTestInit(fd) && !isXTest:
				pkg.HasInit = true
			}
		}
	}

	g.testPackages[testPath] = pkg
	return nil
}

func (g *Generator) GenerateMain(ctx context.Context, d *dag.DAG) error {
	g.testPackagesMutex.Lock()
	defer g.testPackagesMutex.Unlock()

	id := -1
	nextID := func() string {
		id++
		return fmt.Sprintf("pkg%d", id)
	}
	runnerCtx := runner.Context{}
	for _, pkg := range g.testPackages {
		if len(pkg.Tests) == 0 && len(pkg.Benchmarks) == 0 {
			continue
		}
		t := runner.Target{
			ImportPath: pkg.ImportPath,
			Name:       pkg.Name,
			TestName:   nextID(),
			XTestName:  nextID(),
			Directory:  pkg.Dir,
		}

		switch {
		case pkg.Test != nil && pkg.Test.ImportPath == pkg.TestMain:
			t.ImportTest = true
			t.Main = t.TestName + ".TestMain"
		case pkg.XTest != nil && pkg.XTest.ImportPath == pkg.TestMain:
			t.ImportXTest = true
			t.Main = t.XTestName + ".TestMain"
		default:
			t.Main = "defaultMain"
		}

		t.InitFunc = "func(){}"
		t.XInitFunc = "func(){}"
		if pkg.Test != nil && pkg.HasInit {
			t.ImportTest = true
			t.InitFunc = t.TestName + ".GopherTestInit"
		}
		if pkg.XTest != nil && pkg.HasXInit {
			t.ImportXTest = true
			t.XInitFunc = t.XTestName + ".GopherTestInit"
		}

		for _, testFunc := range pkg.Tests {
			switch {
			case pkg.Test != nil && pkg.Test.ImportPath == testFunc.ImportPath:
				t.ImportTest = true
				t.Tests = append(t.Tests, runner.Test{
					Package: t.TestName,
					Name:    testFunc.Name,
				})
			case pkg.XTest != nil && pkg.XTest.ImportPath == testFunc.ImportPath:
				t.ImportXTest = true
				t.Tests = append(t.Tests, runner.Test{
					Package: t.XTestName,
					Name:    testFunc.Name,
				})
			}
		}
		for _, benchmarkFunc := range pkg.Benchmarks {
			switch {
			case pkg.Test != nil && pkg.Test.ImportPath == benchmarkFunc.ImportPath:
				t.ImportTest = true
				t.Benchmarks = append(t.Benchmarks, runner.Test{
					Package: t.TestName,
					Name:    benchmarkFunc.Name,
				})
			case pkg.XTest != nil && pkg.XTest.ImportPath == benchmarkFunc.ImportPath:
				t.ImportXTest = true
				t.Benchmarks = append(t.Benchmarks, runner.Test{
					Package: t.XTestName,
					Name:    benchmarkFunc.Name,
				})
			}
		}
		if t.ImportTest == false && pkg.Test != nil {
			t.ImportTest = true
			t.TestName = "_"
		}
		if t.ImportXTest == false && pkg.XTest != nil {
			t.ImportXTest = true
			t.XTestName = "_"
		}
		runnerCtx.Targets = append(runnerCtx.Targets, t)
	}

	sort.Slice(runnerCtx.Targets, func(i, j int) bool {
		return runnerCtx.Targets[i].ImportPath < runnerCtx.Targets[j].ImportPath
	})

	srcDir := path.Join(g.WorkDir, "main")
	err := os.Mkdir(srcDir, 0777)
	if err != nil {
		return errors.WithStack(err)
	}

	rawImports := []string{}
	for _, pkg := range g.testPackages {
		if pkg.Test != nil {
			rawImports = append(rawImports, pkg.Test.ImportPath)
		}
		if pkg.XTest != nil {
			rawImports = append(rawImports, pkg.XTest.ImportPath)
		}
	}
	rawImports = append(rawImports, runner.Deps...)

	pkg := &packages.Package{
		ImportPath: "main",
		Name:       "main",
		Dir:        srcDir,
		Imports:    rawImports,
	}
	node, err := d.Add(pkg, false, true)
	if err != nil {
		return errors.WithStack(err)
	}
	node.Mutex.Lock()
	defer node.Mutex.Unlock()

	node.GoFiles = append(node.GoFiles, dag.GoFile{
		Dir:       srcDir,
		Filename:  "main.go",
		Generator: &mainGoGenerator{runnerCtx},
	})

	// TODO: Fix dependency
	hasher := &hasher.Hasher{
		BuildCtx: g.BuildCtx,
		Tools:    g.Tools,
	}
	err = hasher.Visit(ctx, node)
	if err != nil {
		return errors.Wrap(err, "hashing main")
	}

	return nil
}

type mainGoGenerator struct {
	runner.Context
}

func (m *mainGoGenerator) Generate(ctx context.Context, node *dag.Node, goFile dag.GoFile, writer io.WriteCloser) error {
	importComplexity := map[string]int64{}
	for _, imported := range node.Imports {
		imported.Mutex.Lock()
		if imported.Intrinsic {
			imported.Mutex.Unlock()
			continue
		}
		importPath := imported.ImportPath
		stat, err := os.Stat(imported.Shlib)
		imported.Mutex.Unlock()
		if err != nil {
			return errors.WithStack(err)
		}
		importComplexity[importPath] = stat.Size()
	}

	for k, v := range m.Targets {
		if v.ImportTest {
			complexity, ok := importComplexity[v.ImportPath]
			if ok {
				v.TestComplexity += complexity
			}
		}
		if v.ImportXTest {
			complexity, ok := importComplexity[v.ImportPath+"_test"]
			if ok {
				v.TestComplexity += complexity
			}
		}
		m.Targets[k] = v
	}

	err := runner.Template.Execute(writer, m.Context)
	if err != nil {
		writer.Close()
		return errors.WithStack(err)
	}

	err = writer.Close()
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}
