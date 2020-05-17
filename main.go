package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	gobuild "go/build"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"runtime"

	"github.com/gophertest/build"

	"github.com/hpidcock/gophertest/cache/hasher"
	"github.com/hpidcock/gophertest/cache/puller"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/packages"
	"github.com/hpidcock/gophertest/runner"
)

var (
	pkgMap       = make(map[string]*packages.Package)
	nodeMap      = make(map[string]*node)
	testPackages = make(map[string]*testPackage)
	workDir      = ""
	cacheDir 	 = ""
	srcDir       = ""
	outFile      = ""
	pkgDir       = path.Join(runtime.GOROOT(), "pkg")
	buildCtx     = gobuild.Default
	tools        = build.DefaultTools
)

var (
	flagStdin       = flag.Bool("stdin", false, "Read package names from stdin")
	flagFile        = flag.String("f", "", "Read package names from file")
	flagPkgDir      = flag.String("p", "", "Group package directory (default is working directory)")
	flagOut         = flag.String("o", "gopher.test", "Output binary")
	flagKeepWorkDir = flag.Bool("keep-work-dir", false, "Prints out work dir and doesn't delete it")
	flagLogBuild    = flag.Bool("x", false, "Log build commands")
)

func main() {
	var err error

	buildCtx.GOARCH = env("GOARCH", buildCtx.GOARCH)
	buildCtx.GOOS = env("GOOS", buildCtx.GOOS)
	// For now we don't support cgo.
	buildCtx.CgoEnabled = false
	buildCtx.UseAllFiles = false
	err = os.Setenv("CGO_ENABLED", "0")
	if err != nil {
		log.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	flag.Parse()

	if *flagPkgDir != "" {
		srcDir = *flagPkgDir
	} else {
		srcDir = wd
	}
	if path.IsAbs(*flagOut) {
		outFile = *flagOut
	} else {
		outFile = path.Join(wd, *flagOut)
	}

	workDir, err = ioutil.TempDir("", "gophertest")
	if err != nil {
		log.Fatal(err)
	}

	if *flagKeepWorkDir {
		log.Printf("workDir=%s", workDir)
	}

	if *flagLogBuild {
		build.DebugLog = true
	}

	remaining := flag.NArg()
	inputTypes := 0
	testPackages := []string{}
	if *flagStdin {
		inputTypes++
		testPackages, err = readPackages(os.Stdin)
		if err != nil {
			log.Fatal(err)
		}
	}
	if *flagFile != "" {
		inputTypes++
		f, err := os.Open(*flagFile)
		if err != nil {
			log.Fatal(err)
		}
		testPackages, err = readPackages(f)
		if err != nil {
			log.Fatal(err)
		}
		err = f.Close()
		if err != nil {
			log.Fatal(err)
		}
	}
	if remaining > 0 {
		inputTypes++
		testPackages = os.Args[len(os.Args)-remaining:]
	}
	if inputTypes != 1 {
		fmt.Fprintf(os.Stderr, "only one of -f or -stdin or command line packages can be passed")
		flag.PrintDefaults()
		os.Exit(-1)
	}
	if len(testPackages) == 0 {
		fmt.Fprintf(os.Stderr, "no packages to build")
		os.Exit(-1)
	}

	fullPackages := append([]string(nil), testPackages...)
	fullPackages = append(fullPackages, runner.Deps...)
	buildPkgs, err := packages.ImportAll(buildCtx, srcDir, fullPackages)

	testPackagesMap := map[string]struct{}{}
	for _, importPath := range testPackages {
		testPackagesMap[importPath] = struct{}{}
	}

	d := dag.NewDAG()
	for _, pkg := range buildPkgs {
		_, includeTests := testPackagesMap[pkg.ImportPath]
		_, err := d.Add(pkg, includeTests)
		if err != nil {
			log.Fatal(err)
		}
	}

	err = d.CheckComplete()
	if err != nil {
		log.Fatal(err)
	}

	err = d.VisitAllFromRight(context.Background(), &hasher.Hasher{
		BuildCtx: buildCtx,
		Tools:    tools,
	})
	if err != nil {
		log.Fatal(err)
	}

	err = d.VisitAllFromRight(context.Background(), &puller.Puller{
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
		CacheDir: 
	})
	if err != nil {
		log.Fatal(err)
	}

	d.VisitAllFromLeft(context.Background(), dag.VisitorFunc(func(ctx context.Context, node *dag.Node) error {
		if node.ImportPath == "github.com/juju/juju/featuretests" {
			fmt.Println(node.Meta[0].(*hasher.HashMeta).BuildID)
		}
		return nil
	}))

	/*err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(func(ctx context.Context, n *dag.Node) error {
		fmt.Printf("%s %s\n", n.ImportPath, n.Meta[0].(*cacher.Cache).BuildID)
		return nil
	}))
	if err != nil {
		log.Fatal(err)
	}*/

	/*
		for _, pkg := range buildPkgs {
			pkgMap[pkg.ImportPath] = pkg
		}

		for _, pkg := range packages {
			err := add(pkg, srcDir, true)
			if err != nil {
				log.Fatal(err)
			}
		}

		err = patchTests()
		if err != nil {
			log.Fatal(err)
		}

		err = generateMain()
		if err != nil {
			log.Fatal(err)
		}

		linkDeps()

		err = buildAll()
		if err != nil {
			log.Fatal(err)
		}

		err = link()
		if err != nil {
			log.Fatal(err)
		}
	*/
	if !*flagKeepWorkDir {
		err = os.RemoveAll(workDir)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func readPackages(r io.Reader) ([]string, error) {
	reader := bufio.NewReader(r)
	line := []byte(nil)
	packages := []string(nil)
	for {
		buf, isPrefix, err := reader.ReadLine()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		line = append(line, buf...)
		if isPrefix {
			continue
		}
		packages = append(packages, string(line))
		line = nil
	}
	if line != nil {
		packages = append(packages, string(line))
	}
	return packages, nil
}
