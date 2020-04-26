# Gophertest :fire:

Go-for-test speeds up testing large Go projects by building once and running tests concurrently.

## Who is this for

- Projects that have a lot of test packages :ant:
- Projects that have a lot of long running tests :snail:
- Projects that have a lot of shared dependencies :deciduous_tree:

## Requirements

To be able to test your project it must currently conform to these requirements:
- Build without Cgo (pure go)

## Installing

`go get github.com/hpidcock/gophertest`

## Running

Gophertest tool builds a test binary similar to `go test -c` but the difference is that it shares a single binary across multiple test packages.

To build and run a test binary for project `github.com/x/y`:
- change working directory to the project root
- `$ gophertest github.com/x/y/first github.com/x/y/second`
- `$ ./gopher.test`

### Passing test packages to gophertest

You can pass test package paths to `gophertest` three ways.
Either by trailing command line args, a file or via stdin.
```
$ gophertest
only one of -f or -stdin or command line packages can be passed
  -f string
    	Read package names from file
  -o string
    	Output binary (default "gopher.test")
  -p string
    	Group package directory (default is working directory)
  -stdin
    	Read package names from stdin
```

To pass all packages of a project you can use `go list` and pipe that to `gophertest`.
```
$ go list github.com/x/y/... | gophertest
```

### Passing arguments to built test binary

All arguments passed to the test binary are used in invocations to each test. You can use all the flags your test package wants or even the standard flags usable when compiling a test package with `go test -c`.

### Running a single test package

You can still and should use `go test` when you want to run a single package. In the case you want to run a single package you can use the `GOPHERTEST_PKG` environment variable to pass the package you want to run.

```
$ GOPHERTEST_PKG="github.com/x/y/first" ./gopher.test
```

### Concurrent test running

By default test binaries produced by `gophertest` will not run concurrently. To do this pass the `GOPHERTEST_CONCURRENT` environment variable when running tests. It can be any number, but its recommended to use small numbers close to the number of CPU cores or threads available.

```
$ GOPHERTEST_CONCURRENT=8 ./gopher.test
```

*NOTE: When running tests in concurrent mode, test output will be buffered in memory and written to stdout when the test completes. For this reason all stderr will be blended with stdout. It should be kept in mind that test output in this mode is buffered to memory, so large test ouput may consume too much memory and should be avoided.*

## Todo :squirrel:

- Support cgo and cross-compilation
- Embedding source and testdata
