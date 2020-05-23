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
	"github.com/pkg/errors"

	"github.com/hpidcock/gophertest/builder"
	"github.com/hpidcock/gophertest/cache/hasher"
	"github.com/hpidcock/gophertest/cache/puller"
	"github.com/hpidcock/gophertest/cache/storer"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/deferredinit"
	"github.com/hpidcock/gophertest/linker"
	"github.com/hpidcock/gophertest/maingen"
	"github.com/hpidcock/gophertest/maingen/runner"
	"github.com/hpidcock/gophertest/packages"
	"github.com/hpidcock/gophertest/util"
)

var (
	pkgMap   = make(map[string]*packages.Package)
	workDir  = ""
	cacheDir = ""
	srcDir   = ""
	outFile  = ""
	pkgDir   = path.Join(runtime.GOROOT(), "pkg")
	buildCtx = gobuild.Default
	tools    = build.DefaultTools
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
	err := Main()
	if err != nil {
		fmt.Printf("%+v", err)
	}
}

func Main() error {
	var err error

	buildCtx.GOARCH = env("GOARCH", buildCtx.GOARCH)
	buildCtx.GOOS = env("GOOS", buildCtx.GOOS)
	// For now we don't support cgo.
	buildCtx.CgoEnabled = false
	buildCtx.UseAllFiles = false
	err = os.Setenv("CGO_ENABLED", "0")
	if err != nil {
		return errors.WithStack(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		return errors.WithStack(err)
	}

	cacheDir, err = util.CacheDir(buildCtx)
	if err != nil {
		return errors.Wrap(err, "creating cache dir")
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
		return errors.Wrap(err, "creating work directory")
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
			return errors.Wrap(err, "reading packages from stdin")
		}
	}
	if *flagFile != "" {
		inputTypes++
		f, err := os.Open(*flagFile)
		if err != nil {
			return errors.Wrapf(err, "opening package file %q", *flagFile)
		}
		testPackages, err = readPackages(f)
		if err != nil {
			return errors.Wrapf(err, "reading packages from %q", *flagFile)
		}
		err = f.Close()
		if err != nil {
			return errors.Wrap(err, "closing file")
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

	lock, err := util.LockDirectory(cacheDir)
	if err != nil {
		return errors.Wrapf(err, "locking cache dir %q", cacheDir)
	}
	defer lock.Unlock()

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
			return errors.Wrapf(err, "adding %q to dag", pkg.ImportPath)
		}
	}

	err = d.CheckComplete()
	if err != nil {
		return errors.Wrap(err, "dag incomplete")
	}

	err = d.VisitAllFromRight(context.Background(), &hasher.Hasher{
		BuildCtx: buildCtx,
		Tools:    tools,
	})
	if err != nil {
		return errors.Wrap(err, "hashing source")
	}

	err = d.VisitAllFromRight(context.Background(), &puller.Puller{
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
		CacheDir: cacheDir,
	})
	if err != nil {
		return errors.Wrap(err, "pulling from cache")
	}

	di := &deferredinit.DeferredIniter{
		BuildCtx:  buildCtx,
		Tools:     tools,
		WorkDir:   workDir,
		SourceDir: srcDir,
	}
	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(di.CollectPackages))
	if err != nil {
		return errors.Wrap(err, "finding tests")
	}

	err = di.LoadPackages()
	if err != nil {
		return errors.Wrap(err, "loading tests")
	}

	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(di.Rewrite))
	if err != nil {
		return errors.Wrap(err, "rewriting tests")
	}

	err = d.CheckComplete()
	if err != nil {
		return errors.Wrap(err, "dag incomplete")
	}

	gen := &maingen.Generator{
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
	}

	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(gen.FindTests))
	if err != nil {
		return errors.Wrap(err, "finding tests")
	}

	err = gen.GenerateMain(context.Background(), d)
	if err != nil {
		return errors.Wrap(err, "generating main")
	}

	err = d.VisitAllFromRight(context.Background(), &builder.Builder{
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
	})
	if err != nil {
		return errors.Wrap(err, "compiling")
	}

	err = d.VisitAllFromRight(context.Background(), &linker.Linker{
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
		OutFile:  outFile,
	})
	if err != nil {
		return errors.Wrap(err, "linking")
	}

	err = d.VisitAllFromRight(context.Background(), &storer.Storer{
		BuildCtx: buildCtx,
		Tools:    tools,
		CacheDir: cacheDir,
	})
	if err != nil {
		return errors.Wrap(err, "updating cache")
	}

	if !*flagKeepWorkDir {
		err = os.RemoveAll(workDir)
		if err != nil {
			return errors.Wrap(err, "cleaning work dir")
		}
	}

	return nil
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
			return nil, errors.WithStack(err)
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
