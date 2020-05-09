package dag

import (
	"sync"
)

type Node struct {
	*NodeBits
	ImportPath string
	Deps       []*Node

	Mutex sync.Mutex
}

type NodeBits struct {
	Name string

	SourceDir string
	RootDir   string
	Goroot    bool

	Shlib    string
	Standard bool

	GoFiles []GoFile
	SFiles  []SFile

	Imports []Import
}

type Import struct {
	*Node
	Test bool
}

type GoFile struct {
	Dir      string
	Filename string
	Test     bool
}

type SFile struct {
	Dir      string
	Filename string
}
