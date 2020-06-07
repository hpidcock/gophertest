package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	gobuild "go/build"
	"io"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/gophertest/build"
	"github.com/pkg/errors"

	"github.com/hpidcock/gophertest/builder"
	"github.com/hpidcock/gophertest/cache/hasher"
	"github.com/hpidcock/gophertest/cache/puller"
	"github.com/hpidcock/gophertest/cache/storer"
	"github.com/hpidcock/gophertest/dag"
	"github.com/hpidcock/gophertest/deferredinit"
	"github.com/hpidcock/gophertest/linker"
	"github.com/hpidcock/gophertest/logging"
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
	flagStdin           = flag.Bool("stdin", false, "read package names from stdin")
	flagFile            = flag.String("f", "", "read package names from file")
	flagPkgDir          = flag.String("p", "", "group package directory (default is working directory)")
	flagOut             = flag.String("o", "gopher.test", "output binary")
	flagKeepWorkDir     = flag.Bool("keep-work-dir", false, "prints out work dir and doesn't delete it")
	flagLogBuild        = flag.Bool("x", false, "log build commands")
	flagIgnoreCache     = flag.Bool("a", false, "force rebuilding")
	flagSkipCacheUpdate = flag.Bool("u", false, "skip cache update")
	flagVerbose         = flag.Bool("v", false, "verbose logging")
	flagGraph           = flag.String("g", "", "output graph to file and exit")
	flagGraphNodes      = flag.String("gn", "", "node keys to graph comma seperated")
)

func main() {
	err := Main()
	if err != nil {
		fmt.Printf("%+v", err)
	}
}

func Main() (errOut error) {
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

	logger := logging.Logger(nil)
	if *flagVerbose {
		logger = &logging.StdLogger{}
	} else {
		logger = &logging.NullLogger{}
	}

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
		logger.Infof("workDir=%s", workDir)
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
		fmt.Fprintf(os.Stderr, "only one of -f or -stdin or command line packages can be passed\n")
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

	runtime.GC()
	logger.Infof("importing packages")
	fullPackages := append([]string(nil), testPackages...)
	fullPackages = append(fullPackages, runner.Deps...)
	buildPkgs, err := packages.ImportAll(buildCtx, srcDir, fullPackages)

	testPackagesMap := map[string]struct{}{}
	for _, importPath := range testPackages {
		testPackagesMap[importPath] = struct{}{}
	}

	runtime.GC()
	logger.Infof("graphing packages")
	d := dag.NewDAG(logger)
	for _, pkg := range buildPkgs {
		_, includeTests := testPackagesMap[pkg.ImportPath]
		_, err := d.Add(pkg, includeTests, false)
		if err != nil {
			return errors.Wrapf(err, "adding %q to dag", pkg.ImportPath)
		}
	}

	runtime.GC()
	logger.Infof("validating dag")
	err = d.CheckComplete(false)
	if err != nil {
		return errors.Wrap(err, "dag incomplete")
	}

	runtime.GC()
	logger.Infof("checking for cycles")
	err = d.CheckForCycles()
	if err != nil {
		return errors.WithStack(err)
	}
	err = d.CheckForCycles()
	if err != nil {
		return errors.WithStack(err)
	}

	if *flagGraph != "" {
		return d.Graph(*flagGraph, strings.Split(*flagGraphNodes, ","))
	}

	runtime.GC()
	logger.Infof("validating dag")
	err = d.CheckComplete(true)
	if err != nil {
		return errors.Wrap(err, "dag incomplete")
	}

	runtime.GC()
	logger.Infof("hashing packages")
	err = d.VisitAllFromRight(context.Background(), &hasher.Hasher{
		Logger:   logger,
		BuildCtx: buildCtx,
		Tools:    tools,
	})
	if err != nil {
		return errors.Wrap(err, "hashing source")
	}

	if *flagIgnoreCache == false {
		logger.Infof("loading cache")
		pull := &puller.Puller{
			Logger:   logger,
			BuildCtx: buildCtx,
			Tools:    tools,
			WorkDir:  workDir,
			CacheDir: cacheDir,
		}
		err = d.VisitAll(context.Background(), pull, runtime.NumCPU())
		if err != nil {
			return errors.Wrap(err, "pulling from cache")
		}
	} else {
		logger.Infof("skipping cache")
	}

	runtime.GC()
	di := &deferredinit.DeferredIniter{
		Logger:    logger,
		BuildCtx:  buildCtx,
		Tools:     tools,
		WorkDir:   workDir,
		SourceDir: srcDir,
	}
	logger.Infof("collecting packages for deferred init")
	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(di.CollectPackages))
	if err != nil {
		return errors.Wrap(err, "finding tests")
	}

	logger.Infof("loading packages for deferred init")
	err = di.LoadPackages()
	if err != nil {
		return errors.Wrap(err, "loading tests")
	}

	logger.Infof("rewriting packages for deferred init")
	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(di.Rewrite))
	if err != nil {
		return errors.Wrap(err, "rewriting tests")
	}

	runtime.GC()
	logger.Infof("validating dag")
	err = d.CheckComplete(true)
	if err != nil {
		return errors.Wrap(err, "dag incomplete")
	}

	gen := &maingen.Generator{
		Logger:   logger,
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
	}

	runtime.GC()
	logger.Infof("finding tests")
	err = d.VisitAllFromRight(context.Background(), dag.VisitorFunc(gen.FindTests))
	if err != nil {
		return errors.Wrap(err, "finding tests")
	}

	logger.Infof("generating test main")
	err = gen.GenerateMain(context.Background(), d)
	if err != nil {
		return errors.Wrap(err, "generating main")
	}
	gen = nil

	defer func() {
		if !*flagSkipCacheUpdate {
			storer := &storer.Storer{
				Logger:   logger,
				BuildCtx: buildCtx,
				Tools:    tools,
				CacheDir: cacheDir,
			}
			logger.Infof("storing build result in cache")
			err = d.VisitAll(context.Background(), storer, runtime.NumCPU())
			if err != nil {
				err = errors.Wrap(err, "updating cache")
				if errOut != nil {
					fmt.Println(err.Error())
				} else {
					errOut = err
				}
				return
			}
		}

		if !*flagKeepWorkDir {
			logger.Infof("cleanup work dir")
			err := os.RemoveAll(workDir)
			if err != nil {
				err = errors.Wrap(err, "cleaning work dir")
				if errOut != nil {
					fmt.Println(err.Error())
				} else {
					errOut = err
				}
				return
			}
		}
	}()

	runtime.GC()
	logger.Infof("building packages")
	err = d.VisitAllFromRight(context.Background(), &builder.Builder{
		Logger:   logger,
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
	})
	if err != nil {
		return errors.Wrap(err, "compiling")
	}

	runtime.GC()
	logger.Infof("linking executable")
	err = d.VisitAllFromRight(context.Background(), &linker.Linker{
		Logger:   logger,
		BuildCtx: buildCtx,
		Tools:    tools,
		WorkDir:  workDir,
		OutFile:  outFile,
	})
	if err != nil {
		return errors.Wrap(err, "linking")
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
