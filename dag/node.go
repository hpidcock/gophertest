package dag

import (
	"context"
	"io"
	"sync"
)

type Flags uint32

const (
	Visited Flags = 1 << iota
)

type Node struct {
	*NodeBits
	ImportPath string
	Deps       []*Node

	Mutex sync.RWMutex

	flags          Flags
	flagGeneration int
}

type NodeBits struct {
	Name      string
	Tests     bool
	SourceDir string
	RootDir   string
	Goroot    bool
	Standard  bool
	Intrinsic bool
	GoFiles   []GoFile
	SFiles    []SFile
	Imports   []Import
	ImportMap map[string]string

	Shlib string
	Meta  []interface{}
}

type Import struct {
	*Node
	Test bool
}

type GoFile struct {
	Dir       string
	Filename  string
	Test      bool
	Generator Generator
}

type SFile struct {
	Dir      string
	Filename string
}

type Generator interface {
	Generate(context.Context, *Node, GoFile, io.WriteCloser) error
}

type GeneratorFunc func(context.Context, *Node, GoFile, io.WriteCloser) error

func (g GeneratorFunc) Generate(ctx context.Context, node *Node, goFile GoFile, writer io.WriteCloser) error {
	return g(ctx, node, goFile, writer)
}
