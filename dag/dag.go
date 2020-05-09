package dag

import (
	"context"
	"fmt"
	"sync"

	"github.com/hpidcock/gophertest/buildctx"
	"golang.org/x/sync/errgroup"
)

type DAG struct {
	// leftLeaf is nodes without dependants
	leftLeaf map[string]*Node
	// rightLeaf is nodes without imports
	rightLeaf map[string]*Node
	nodes     map[string]*Node

	mutex sync.Mutex
}

func NewDAG() *DAG {
	return &DAG{
		leftLeaf:  make(map[string]*Node),
		rightLeaf: make(map[string]*Node),
		nodes:     make(map[string]*Node),
	}
}

func (d *DAG) Add(pkg *buildctx.Package, includeTests bool) (*Node, error) {
	importPath := pkg.ImportPath
	node := d.Obtain(importPath)
	defer node.Mutex.Unlock()

	if node.NodeBits != nil {
		return nil, fmt.Errorf("package %q already has bits", importPath)
	}

	bits := &NodeBits{
		Name:      pkg.Name,
		SourceDir: pkg.Dir,
		RootDir:   pkg.Root,
		Goroot:    pkg.Goroot,
		Standard:  pkg.Standard,
	}
	node.NodeBits = bits

	for _, f := range pkg.GoFiles {
		goFile := GoFile{
			Dir:      pkg.Dir,
			Filename: f,
			Test:     false,
		}
		bits.GoFiles = append(bits.GoFiles, goFile)
	}
	if includeTests {
		for _, f := range pkg.TestGoFiles {
			goFile := GoFile{
				Dir:      pkg.Dir,
				Filename: f,
				Test:     true,
			}
			bits.GoFiles = append(bits.GoFiles, goFile)
		}
	}
	for _, f := range pkg.SFiles {
		sFile := SFile{
			Dir:      pkg.Dir,
			Filename: f,
		}
		bits.SFiles = append(bits.SFiles, sFile)
	}

	alreadyImported := map[string]struct{}{}
	if includeTests {
		for _, imported := range pkg.TestImports {
			if _, ok := alreadyImported[imported]; ok {
				continue
			}
			alreadyImported[imported] = struct{}{}
			importedNode := d.Obtain(imported)
			importedNode.Deps = append(importedNode.Deps, node)
			importedNode.Mutex.Unlock()
			delete(d.leftLeaf, imported)
			bits.Imports = append(bits.Imports, Import{
				Node: importedNode,
				Test: true,
			})
		}
	}
	for _, imported := range pkg.Imports {
		if _, ok := alreadyImported[imported]; ok {
			continue
		}
		alreadyImported[imported] = struct{}{}
		importedNode := d.Obtain(imported)
		importedNode.Deps = append(importedNode.Deps, node)
		importedNode.Mutex.Unlock()
		delete(d.leftLeaf, imported)
		bits.Imports = append(bits.Imports, Import{
			Node: importedNode,
			Test: false,
		})
	}

	if includeTests && len(pkg.XTestGoFiles) > 0 {
		importPathX := importPath + "_test"
		nodeX := d.Obtain(importPathX)
		defer nodeX.Mutex.Unlock()

		if nodeX.NodeBits != nil {
			return nil, fmt.Errorf("package %q already has bits", importPathX)
		}

		bitsX := &NodeBits{
			Name:      pkg.Name + "_test",
			SourceDir: pkg.Dir,
			RootDir:   pkg.Root,
			Goroot:    pkg.Goroot,
			Standard:  pkg.Standard,
		}
		nodeX.NodeBits = bitsX

		for _, f := range pkg.XTestGoFiles {
			goFile := GoFile{
				Dir:      pkg.Dir,
				Filename: f,
				Test:     true,
			}
			bitsX.GoFiles = append(bitsX.GoFiles, goFile)
		}

		alreadyImportedX := map[string]struct{}{}
		for _, imported := range pkg.XTestImports {
			if _, ok := alreadyImportedX[imported]; ok {
				continue
			}
			alreadyImportedX[imported] = struct{}{}
			var importedNode *Node
			if imported == importPath {
				importedNode = node
			} else {
				importedNode = d.Obtain(imported)
			}
			importedNode.Deps = append(importedNode.Deps, nodeX)
			if imported != importPath {
				importedNode.Mutex.Unlock()
			}
			delete(d.leftLeaf, imported)
			bitsX.Imports = append(bitsX.Imports, Import{
				Node: importedNode,
				Test: true,
			})
		}

		if len(bitsX.Imports) == 0 {
			d.rightLeaf[importPath] = nodeX
		} else {
			delete(d.rightLeaf, importPathX)
		}

		if len(nodeX.Deps) == 0 {
			d.leftLeaf[importPathX] = nodeX
		} else {
			delete(d.leftLeaf, importPathX)
		}
	}

	if len(bits.Imports) == 0 {
		d.rightLeaf[importPath] = node
	} else {
		delete(d.rightLeaf, importPath)
	}

	if len(node.Deps) == 0 {
		d.leftLeaf[importPath] = node
	} else {
		delete(d.leftLeaf, importPath)
	}

	return node, nil
}

// Obtain a Node by finding it or creating it and lock it.
// Callers of Obtain must release the lock.
func (d *DAG) Obtain(importPath string) *Node {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	node, ok := d.nodes[importPath]
	if ok {
		node.Mutex.Lock()
		return node
	}
	node = &Node{
		ImportPath: importPath,
	}
	node.Mutex.Lock()
	d.nodes[importPath] = node
	return node
}

func (d *DAG) CheckComplete() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	for _, node := range d.nodes {
		var err error
		node.Mutex.Lock()
		if node.NodeBits == nil {
			err = fmt.Errorf("node %q is incomplete", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return err
		}
	}

	for _, node := range d.leftLeaf {
		var err error
		node.Mutex.Lock()
		if len(node.Deps) != 0 {
			err = fmt.Errorf("node %q is marked as a left leaf but has dependants", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return err
		}
	}

	for _, node := range d.rightLeaf {
		var err error
		node.Mutex.Lock()
		if len(node.Imports) != 0 {
			err = fmt.Errorf("node %q is marked as a right leaf but has imports", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return err
		}
	}

	return nil
}

// VisitAllFromLeft visits each node from the left.
// NOTE: do not Lock/Unlock the node, VisitAllFromLeft will Lock it for you.
func (d *DAG) VisitAllFromLeft(ctx context.Context, v Visitor) error {
	var pass []*Node
	d.mutex.Lock()
	for _, n := range d.leftLeaf {
		pass = append(pass, n)
	}
	d.mutex.Unlock()
	alreadyAdded := map[*Node]struct{}{}
	return d.visitAll(ctx, v, pass, Right, alreadyAdded)
}

// VisitAllFromRight visits each node from the left.
// NOTE: do not Lock/Unlock the node, VisitAllFromRight will Lock it for you.
func (d *DAG) VisitAllFromRight(ctx context.Context, v Visitor) error {
	var pass []*Node
	d.mutex.Lock()
	for _, n := range d.rightLeaf {
		pass = append(pass, n)
	}
	d.mutex.Unlock()
	alreadyAdded := map[*Node]struct{}{}
	return d.visitAll(ctx, v, pass, Left, alreadyAdded)
}

func (d *DAG) visitAll(ctx context.Context,
	v Visitor,
	pass []*Node,
	direction VisitDirection,
	alreadyAdded map[*Node]struct{},
) error {
	if len(pass) == 0 {
		return nil
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range pass {
		node := n
		node.Mutex.Lock()
		eg.Go(func() error {
			defer node.Mutex.Unlock()
			return v.Visit(egCtx, node, direction)
		})
	}

	err := eg.Wait()
	if err != nil {
		return err
	}

	var nextPass []*Node
	d.mutex.Lock()
	for _, node := range pass {
		var err error
		node.Mutex.Lock()
		switch direction {
		case Left:
			for _, dep := range node.Deps {
				if _, ok := alreadyAdded[dep]; ok {
					continue
				}
				alreadyAdded[dep] = struct{}{}
				nextPass = append(nextPass, dep)
			}
		case Right:
			for _, imported := range node.Imports {
				if _, ok := alreadyAdded[imported.Node]; ok {
					continue
				}
				alreadyAdded[imported.Node] = struct{}{}
				nextPass = append(nextPass, imported.Node)
			}
		default:
			err = fmt.Errorf("invalid direction %v", direction)
		}
		node.Mutex.Unlock()
		if err != nil {
			d.mutex.Unlock()
			return err
		}
	}
	d.mutex.Unlock()

	return d.visitAll(ctx, v, nextPass, direction, alreadyAdded)
}
