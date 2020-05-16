package dag

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/hpidcock/gophertest/packages"
)

type DAG struct {
	// leftLeaf is nodes without dependants
	leftLeaf map[*Node]*Node
	// rightLeaf is nodes without imports
	rightLeaf map[*Node]*Node
	nodes     map[string]*Node

	mutex sync.Mutex
}

func NewDAG() *DAG {
	return &DAG{
		leftLeaf:  make(map[*Node]*Node),
		rightLeaf: make(map[*Node]*Node),
		nodes:     make(map[string]*Node),
	}
}

func (d *DAG) Remove(importPath string) error {
	node := d.Find(importPath)
	if node == nil {
		return fmt.Errorf("node %q not found", importPath)
	}
	defer node.Mutex.Unlock()

	if node.NodeBits != nil {
		for _, imported := range node.Imports {
			imported.Mutex.Lock()
			for i, n := range imported.Deps {
				if n == node {
					if last := len(imported.Deps) - 1; last > 0 {
						imported.Deps[i] = imported.Deps[last]
						imported.Deps = imported.Deps[:last]
					} else {
						imported.Deps = nil
					}
					break
				}
			}
			imported.Mutex.Unlock()
		}
		node.NodeBits = nil
	}

	d.mutex.Lock()
	delete(d.rightLeaf, node)
	if len(node.Deps) == 0 {
		delete(d.leftLeaf, node)
		delete(d.nodes, importPath)
	}
	d.mutex.Unlock()
	return nil
}

func (d *DAG) Add(pkg *packages.Package, includeTests bool) (*Node, error) {
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
		ImportMap: pkg.ImportMap,
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
			bits.Tests = true
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
	if includeTests && bits.Tests {
		for _, imported := range pkg.TestImports {
			if _, ok := alreadyImported[imported]; ok {
				continue
			}
			alreadyImported[imported] = struct{}{}
			importedNode := d.Obtain(imported)
			importedNode.Deps = append(importedNode.Deps, node)
			importedNode.Mutex.Unlock()
			d.mutex.Lock()
			delete(d.leftLeaf, importedNode)
			d.mutex.Unlock()
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
		d.mutex.Lock()
		delete(d.leftLeaf, importedNode)
		d.mutex.Unlock()
		bits.Imports = append(bits.Imports, Import{
			Node: importedNode,
			Test: false,
		})
	}

	if includeTests && len(pkg.XTestGoFiles) > 0 {
		importPathX := pkg.ImportPath + "_test"
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
			Tests:     true,
			ImportMap: pkg.ImportMap,
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
			if imported == pkg.ImportPath {
				importedNode = node
			} else {
				importedNode = d.Obtain(imported)
			}
			importedNode.Deps = append(importedNode.Deps, nodeX)
			if imported != pkg.ImportPath {
				importedNode.Mutex.Unlock()
			}
			d.mutex.Lock()
			delete(d.leftLeaf, importedNode)
			d.mutex.Unlock()
			bitsX.Imports = append(bitsX.Imports, Import{
				Node: importedNode,
				Test: true,
			})
		}

		d.mutex.Lock()
		if len(bitsX.Imports) == 0 {
			d.rightLeaf[nodeX] = nodeX
		} else {
			delete(d.rightLeaf, nodeX)
		}
		if len(nodeX.Deps) == 0 {
			d.leftLeaf[nodeX] = nodeX
		} else {
			delete(d.leftLeaf, nodeX)
		}
		d.mutex.Unlock()
	}

	d.mutex.Lock()
	if len(bits.Imports) == 0 {
		d.rightLeaf[node] = node
	} else {
		delete(d.rightLeaf, node)
	}
	if len(node.Deps) == 0 {
		d.leftLeaf[node] = node
	} else {
		delete(d.leftLeaf, node)
	}
	d.mutex.Unlock()

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

// Find a Node by finding it and lock it.
// Callers of Find must release the lock.
func (d *DAG) Find(importPath string) *Node {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if node, ok := d.nodes[importPath]; ok {
		node.Mutex.Lock()
		return node
	}
	return nil
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

	d.mutex.Unlock()
	countRight := int64(0)
	err := d.VisitAllFromRight(context.Background(), VisitorFunc(func(ctx context.Context, n *Node) error {
		atomic.AddInt64(&countRight, 1)
		return nil
	}))
	d.mutex.Lock()
	if err != nil {
		return err
	}

	if int(countRight) != len(d.nodes) {
		return fmt.Errorf("unable to visit all nodes from right")
	}

	d.mutex.Unlock()
	countLeft := int64(0)
	err = d.VisitAllFromRight(context.Background(), VisitorFunc(func(ctx context.Context, n *Node) error {
		atomic.AddInt64(&countLeft, 1)
		return nil
	}))
	d.mutex.Lock()
	if err != nil {
		return err
	}

	if int(countLeft) != len(d.nodes) {
		return fmt.Errorf("unable to visit all nodes from left")
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
	alreadyVisited := map[*Node]struct{}{}
	return d.visitAll(ctx, v, pass, Right, alreadyAdded, alreadyVisited)
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
	alreadyVisited := map[*Node]struct{}{}
	return d.visitAll(ctx, v, pass, Left, alreadyAdded, alreadyVisited)
}

func (d *DAG) visitAll(ctx context.Context,
	v Visitor,
	pass []*Node,
	direction VisitDirection,
	alreadyAdded map[*Node]struct{},
	alreadyVisited map[*Node]struct{},
) error {
	if len(pass) == 0 {
		return nil
	}

	var nextPass []*Node
	var thisPass []*Node

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range pass {
		node := n
		node.Mutex.Lock()
		ready := true
		switch direction {
		case Left:
			for _, imported := range node.Imports {
				if _, ok := alreadyVisited[imported.Node]; !ok {
					ready = false
					break
				}
			}
		case Right:
			if node.NodeBits == nil {
				break
			}
			for _, dep := range node.Deps {
				if _, ok := alreadyVisited[dep]; !ok {
					ready = false
					break
				}
			}
		}
		if !ready {
			node.Mutex.Unlock()
			nextPass = append(nextPass, n)
			continue
		}
		alreadyVisited[n] = struct{}{}
		thisPass = append(thisPass, n)
		eg.Go(func() error {
			defer node.Mutex.Unlock()
			return v.Visit(egCtx, node)
		})
	}

	err := eg.Wait()
	if err != nil {
		return err
	}

	d.mutex.Lock()
	for _, node := range thisPass {
		node.Mutex.Lock()
		switch direction {
		case Left:
			for _, dep := range node.Deps {
				if _, ok := alreadyAdded[dep]; ok {
					continue
				}
				if _, ok := d.nodes[dep.ImportPath]; !ok {
					continue
				}
				alreadyAdded[dep] = struct{}{}
				nextPass = append(nextPass, dep)
			}
		case Right:
			if node.NodeBits == nil {
				break
			}
			for _, imported := range node.Imports {
				if _, ok := alreadyAdded[imported.Node]; ok {
					continue
				}
				if _, ok := d.nodes[imported.ImportPath]; !ok {
					continue
				}
				alreadyAdded[imported.Node] = struct{}{}
				nextPass = append(nextPass, imported.Node)
			}
		}
		node.Mutex.Unlock()
	}
	d.mutex.Unlock()

	return d.visitAll(ctx, v, nextPass, direction, alreadyAdded, alreadyVisited)
}
