package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/build"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"runtime"
)

var (
	nodeMap      = make(map[string]*node)
	testPackages = make(map[string]*testPackage)
	workDir      = ""
	toolCompile  = path.Join(build.ToolDir, "compile")
	toolLink     = path.Join(build.ToolDir, "link")
	toolAsm      = path.Join(build.ToolDir, "asm")
	toolPack     = path.Join(build.ToolDir, "pack")
	toolCgo      = path.Join(build.ToolDir, "cgo")
	srcDir       = ""
	outFile      = ""
	pkgDir       = path.Join(runtime.GOROOT(), "pkg")
	buildCtx     = build.Default
)

var (
	flagStdin  = flag.Bool("stdin", false, "Read package names from stdin")
	flagFile   = flag.String("f", "", "Read package names from file")
	flagPkgDir = flag.String("p", "", "Group package directory (default is working directory)")
	flagOut    = flag.String("o", "gopher.test", "Output binary")
)

func main() {
	var err error

	buildCtx.GOARCH = env("GOARCH", buildCtx.GOARCH)
	buildCtx.GOOS = env("GOOS", buildCtx.GOOS)
	// For now we don't support cgo.
	buildCtx.CgoEnabled = false
	buildCtx.UseAllFiles = false

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

	remaining := flag.NArg()
	inputTypes := 0
	packages := []string{}
	if *flagStdin {
		inputTypes++
		packages, err = readPackages(os.Stdin)
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
		packages, err = readPackages(f)
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
		packages = os.Args[len(os.Args)-remaining:]
	}
	if inputTypes != 1 {
		fmt.Fprintf(os.Stderr, "only one of -f or -stdin or command line packages can be passed")
		flag.PrintDefaults()
		os.Exit(-1)
	}
	if len(packages) == 0 {
		fmt.Fprintf(os.Stderr, "no packages to build")
		os.Exit(-1)
	}

	for _, pkg := range packages {
		err := add(pkg, srcDir, true)
		if err != nil {
			log.Fatal(err)
		}
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

	err = os.RemoveAll(workDir)
	if err != nil {
		log.Fatal(err)
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
