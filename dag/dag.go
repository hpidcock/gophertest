package dag

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	"github.com/hpidcock/gophertest/packages"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type DAG struct {
	logger Logger
	// leftLeaf is nodes without dependants
	leftLeaf map[*Node]*Node
	// rightLeaf is nodes without imports
	rightLeaf map[*Node]*Node
	nodes     map[NodeKey]*Node

	flagGeneration int

	mutex sync.Mutex
}

func NewDAG(logger Logger) *DAG {
	return &DAG{
		logger:    logger,
		leftLeaf:  make(map[*Node]*Node),
		rightLeaf: make(map[*Node]*Node),
		nodes:     make(map[NodeKey]*Node),
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
		delete(d.nodes, node.NodeKey)
	}
	d.mutex.Unlock()
	return nil
}

func (d *DAG) Add(pkg *packages.Package, includeTests bool, useIsolated bool) (*Node, error) {
	importPath := pkg.ImportPath
	node := d.Obtain(importPath)
	defer node.Mutex.Unlock()

	if node.NodeBits != nil {
		return nil, fmt.Errorf("package %q already has bits", importPath)
	}

	findOrObtain := d.Obtain
	if useIsolated {
		findOrObtain = func(importPath string) *Node {
			isolated := d.FindByKey(NodeKey(importPath + "+isolated"))
			if isolated != nil {
				return isolated
			}
			return d.Obtain(importPath)
		}
	}

	bits := &NodeBits{
		Name:      pkg.Name,
		CacheName: pkg.Name,
		SourceDir: pkg.Dir,
		RootDir:   pkg.Root,
		Goroot:    pkg.Goroot,
		Standard:  pkg.Standard,
		ImportMap: pkg.ImportMap,
	}
	switch importPath {
	case "C", "unsafe":
		bits.Intrinsic = true
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
			importedNode := findOrObtain(imported)
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
		importedNode := findOrObtain(imported)
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
		nodeX := findOrObtain(importPathX)
		defer nodeX.Mutex.Unlock()

		if nodeX.NodeBits != nil {
			return nil, fmt.Errorf("package %q already has bits", importPathX)
		}

		bitsX := &NodeBits{
			Name:      pkg.Name + "_test",
			CacheName: pkg.Name + "_test",
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
				importedNode = findOrObtain(imported)
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
	return d.ObtainByKey(NodeKey(importPath), importPath)
}

// ObtainByKey a Node by finding it or creating it and lock it.
// Callers of Obtain must release the lock.
func (d *DAG) ObtainByKey(nodeKey NodeKey, importPath string) *Node {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	node, ok := d.nodes[nodeKey]
	if ok {
		node.Mutex.Lock()
		return node
	}
	node = &Node{
		NodeKey:    nodeKey,
		ImportPath: importPath,
	}
	node.Mutex.Lock()
	d.nodes[nodeKey] = node
	return node
}

// Find a Node by finding it and lock it.
// Callers of Find must release the lock.
func (d *DAG) Find(importPath string) *Node {
	return d.FindByKey(NodeKey(importPath))
}

// FindByKey a Node by finding it and lock it.
// Callers of Find must release the lock.
func (d *DAG) FindByKey(nodeKey NodeKey) *Node {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if node, ok := d.nodes[nodeKey]; ok {
		node.Mutex.Lock()
		return node
	}
	return nil
}

func (d *DAG) CheckComplete(full bool) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.logger.Infof("checking all nodes are loaded")
	for _, node := range d.nodes {
		var err error
		node.Mutex.Lock()
		if node.NodeBits == nil {
			err = fmt.Errorf("node %q is incomplete", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return errors.WithStack(err)
		}
	}

	d.logger.Infof("checking left")
	for _, node := range d.leftLeaf {
		var err error
		node.Mutex.Lock()
		if len(node.Deps) != 0 {
			err = fmt.Errorf("node %q is marked as a left leaf but has dependants", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return errors.WithStack(err)
		}
	}

	d.logger.Infof("checking right")
	for _, node := range d.rightLeaf {
		var err error
		node.Mutex.Lock()
		if len(node.Imports) != 0 {
			err = fmt.Errorf("node %q is marked as a right leaf but has imports", node.ImportPath)
		}
		node.Mutex.Unlock()
		if err != nil {
			return errors.WithStack(err)
		}
	}

	if full {
		d.logger.Infof("checking all nodes are reachable from right")
		d.mutex.Unlock()
		countRight := int64(0)
		err := d.VisitAllFromRight(context.Background(), VisitorFunc(func(ctx context.Context, n *Node) error {
			atomic.AddInt64(&countRight, 1)
			return nil
		}))
		d.mutex.Lock()
		if err != nil {
			return errors.WithStack(err)
		}

		if int(countRight) != len(d.nodes) {
			return fmt.Errorf("unable to visit all nodes from right")
		}

		d.logger.Infof("checking all nodes are reachable from left")
		d.mutex.Unlock()
		countLeft := int64(0)
		err = d.VisitAllFromRight(context.Background(), VisitorFunc(func(ctx context.Context, n *Node) error {
			atomic.AddInt64(&countLeft, 1)
			return nil
		}))
		d.mutex.Lock()
		if err != nil {
			return errors.WithStack(err)
		}

		if int(countLeft) != len(d.nodes) {
			return fmt.Errorf("unable to visit all nodes from left")
		}
	}

	return nil
}

type cycleError struct {
	imports []string
}

func (c *cycleError) Error() string {
	buf := &bytes.Buffer{}
	for i := len(c.imports) - 1; i >= 0; i-- {
		fmt.Fprintln(buf, c.imports[i])
	}
	return buf.String()
}

func (d *DAG) CheckForCycles() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	cycleErrors := []*cycleError(nil)
	for _, node := range d.nodes {
		d.flagGeneration++
		var err error
		node.Mutex.RLock()
		for _, importedNode := range node.Imports {
			err = d.checkForCycles(node, importedNode)
			if c, ok := err.(*cycleError); ok {
				c.imports = append(c.imports, node.ImportPath)
				cycleErrors = append(cycleErrors, c)
				err = nil
			} else if err != nil {
				break
			}
		}
		node.Mutex.RUnlock()
		if err != nil {
			return errors.WithStack(err)
		}
	}
	if len(cycleErrors) > 0 {
		for _, v := range cycleErrors {
			d.logger.Infof("cycle error: %s", v.Error())
		}
		return fmt.Errorf("cycle errors found")
	}
	cyclicNodes := []*Node(nil)
	for _, node := range d.nodes {
		var err error
		node.Mutex.RLock()
		if node.CyclicTests {
			cyclicNodes = append(cyclicNodes, node)
		}
		node.Mutex.RUnlock()
		if err != nil {
			return errors.WithStack(err)
		}
	}

	d.mutex.Unlock()
	for _, node := range cyclicNodes {
		node.Mutex.Lock()

		// Clone Isolated
		isolatedKey := NodeKey(node.ImportPath + "+isolated")
		isolated := d.ObtainByKey(isolatedKey, node.ImportPath)
		isolated.Deps = nil
		copyBits := *node.NodeBits
		isolated.NodeBits = &copyBits
		isolated.CyclicTests = false
		isolated.LinkMode = AlwaysLink
		isolated.Meta = append([]interface{}(nil), isolated.Meta...)
		isolated.GoFiles = append([]GoFile(nil), isolated.GoFiles...)
		isolated.SFiles = append([]SFile(nil), isolated.SFiles...)
		isolated.Imports = append([]Import(nil), isolated.Imports...)
		if isolated.ImportMap != nil {
			copyMap := make(map[string]string)
			for k, v := range isolated.ImportMap {
				copyMap[k] = v
			}
			isolated.ImportMap = copyMap
		}
		for _, v := range isolated.Imports {
			v.Mutex.Lock()
			v.Deps = append(v.Deps, isolated)
			v.Mutex.Unlock()
		}

		node.LinkMode = NeverLink
		node.CyclicTests = false
		node.Tests = false
		node.CacheName = node.CacheName + "_compile"
		if node.GoFiles != nil {
			newGoFiles := []GoFile{}
			for _, v := range node.GoFiles {
				if !v.Test {
					newGoFiles = append(newGoFiles, v)
				}
			}
			node.GoFiles = newGoFiles
		}
		if node.Imports != nil {
			importsToRemove := make(map[*Node]*Node)
			for _, v := range node.Imports {
				if v.Test {
					importsToRemove[v.Node] = v.Node
				}
			}
			for _, v := range node.Imports {
				if !v.Test {
					delete(importsToRemove, v.Node)
				}
			}

			newImports := []Import(nil)
			for _, v := range node.Imports {
				if _, ok := importsToRemove[v.Node]; !ok {
					newImports = append(newImports, v)
					continue
				}
				v.Mutex.Lock()
				newDeps := []*Node(nil)
				for _, dep := range v.Deps {
					if dep == node {
						continue
					}
					newDeps = append(newDeps, dep)
				}
				v.Deps = newDeps
				if len(v.Deps) == 0 {
					d.leftLeaf[v.Node] = v.Node
				} else {
					delete(d.leftLeaf, v.Node)
				}
				v.Mutex.Unlock()
			}
			node.Imports = newImports
		}

		if node.Deps != nil {
			newDeps := []*Node(nil)
			for _, v := range node.Deps {
				if v.ImportPath == node.ImportPath+"_test" {
					v.Mutex.Lock()
					for k, imp := range v.Imports {
						if imp.Node == node {
							imp.Node = isolated
							isolated.Deps = append(isolated.Deps, v)
							v.Imports[k] = imp
						}
					}
					v.Mutex.Unlock()
					continue
				}
				newDeps = append(newDeps, v)
			}
			node.Deps = newDeps
		}

		if len(isolated.Deps) == 0 {
			d.leftLeaf[isolated] = isolated
		} else {
			delete(d.leftLeaf, isolated)
		}
		if len(node.Deps) == 0 {
			d.leftLeaf[node] = node
		} else {
			delete(d.leftLeaf, node)
		}

		if len(isolated.Imports) == 0 {
			d.rightLeaf[isolated] = isolated
		} else {
			delete(d.rightLeaf, isolated)
		}
		if len(node.Imports) == 0 {
			d.rightLeaf[node] = node
		} else {
			delete(d.rightLeaf, node)
		}

		isolated.Mutex.Unlock()
		node.Mutex.Unlock()
	}
	d.mutex.Lock()

	return nil
}

func (d *DAG) checkForCycles(top *Node, node Import) error {
	if top == node.Node {
		return &cycleError{imports: []string{node.ImportPath}}
	}
	node.Mutex.Lock()
	if node.flagGeneration != d.flagGeneration {
		node.flags = 0
		node.flagGeneration = d.flagGeneration
	}
	if node.flags&Visited != 0 {
		node.Mutex.Unlock()
		return nil
	}
	node.flags |= Visited
	importCopy := append(make([]Import, 0, len(node.Imports)), node.Imports...)
	node.Mutex.Unlock()
	for _, imported := range importCopy {
		err := d.checkForCycles(top, imported)
		if c, ok := err.(*cycleError); ok {
			if imported.Test {
				node.Mutex.Lock()
				if !node.CyclicTests {
					d.logger.Infof("package %q needs to be isolated", node.ImportPath)
					node.CyclicTests = true
				}
				node.Mutex.Unlock()
				continue
			}
			c.imports = append(c.imports, node.ImportPath)
			return c
		} else if err != nil {
			return err
		}
	}
	return nil
}

// VisitAll nodes in any order.
func (d *DAG) VisitAll(ctx context.Context, v Visitor, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 1
	}
	d.mutex.Lock()
	defer d.mutex.Unlock()
	slot := make(chan struct{}, concurrency)
	for i := 0; i < concurrency; i++ {
		slot <- struct{}{}
	}
	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range d.nodes {
		node := n
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-slot:
			eg.Go(func() error {
				defer func() {
					slot <- struct{}{}
				}()
				node.Mutex.Lock()
				defer node.Mutex.Unlock()
				return v.Visit(egCtx, node)
			})
		}
	}
	err := eg.Wait()
	if err != nil {
		return errors.WithStack(err)
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
	for {
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
			thisPass = append(thisPass, n)
			eg.Go(func() error {
				defer node.Mutex.Unlock()
				return v.Visit(egCtx, node)
			})
		}

		err := eg.Wait()
		if err != nil {
			return errors.WithStack(err)
		}

		d.mutex.Lock()
		for _, node := range thisPass {
			alreadyVisited[node] = struct{}{}
			node.Mutex.Lock()
			switch direction {
			case Left:
				for _, dep := range node.Deps {
					if _, ok := alreadyAdded[dep]; ok {
						continue
					}
					if _, ok := d.nodes[dep.NodeKey]; !ok {
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
					if _, ok := d.nodes[imported.NodeKey]; !ok {
						continue
					}
					alreadyAdded[imported.Node] = struct{}{}
					nextPass = append(nextPass, imported.Node)
				}
			}
			node.Mutex.Unlock()
		}
		d.mutex.Unlock()

		pass = nextPass
	}
}
