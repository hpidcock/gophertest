package maingen

import (
	"github.com/hpidcock/gophertest/dag"
)

type testPackage struct {
	ImportPath string
	Dir        string
	Name       string
	Test       *dag.Node
	XTest      *dag.Node

	TestMain string
	HasInit  bool
	HasXInit bool

	Benchmarks []benchmark
	Tests      []test
}

type benchmark struct {
	ImportPath string
	Name       string
}

type test struct {
	ImportPath string
	Name       string
}
