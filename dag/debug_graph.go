package dag

import (
	"fmt"
	"io/ioutil"

	"github.com/pkg/errors"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/encoding/dot"
	"gonum.org/v1/gonum/graph/simple"
)

type graphNode struct {
	graph.Node
	n *Node
}

func (g *graphNode) DOTID() string {
	return fmt.Sprintf(`"%s"`, string(g.n.NodeKey))
}

func (d *DAG) Graph(output string, keys []string) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	graph := simple.NewDirectedGraph()
	m := make(map[NodeKey]*graphNode)
	for _, v := range keys {
		node, ok := d.nodes[NodeKey(v)]
		if !ok {
			continue
		}
		gn := &graphNode{
			Node: graph.NewNode(),
			n:    node,
		}
		graph.AddNode(gn)
		m[node.NodeKey] = gn

		node.Mutex.Lock()
		for _, imp := range node.Imports {
			if _, ok := m[imp.NodeKey]; ok {
				continue
			}
			gn := &graphNode{
				Node: graph.NewNode(),
				n:    imp.Node,
			}
			graph.AddNode(gn)
			m[imp.NodeKey] = gn
		}
		for _, dep := range node.Deps {
			if _, ok := m[dep.NodeKey]; ok {
				continue
			}
			gn := &graphNode{
				Node: graph.NewNode(),
				n:    dep,
			}
			graph.AddNode(gn)
			m[dep.NodeKey] = gn
		}
		node.Mutex.Unlock()
	}

	for _, v := range m {
		d.flagGeneration++
		v.n.Mutex.Lock()
		for _, imp := range v.n.Imports {
			if gn, ok := m[imp.NodeKey]; ok {
				e := graph.NewEdge(v, gn)
				graph.SetEdge(e)
			}
			d.recurseGraph(v.n, imp.Node, graph, v, m)
		}
		v.n.Mutex.Unlock()
	}

	b, err := dot.Marshal(graph, "dag", "", "")
	if err != nil {
		return errors.WithStack(err)
	}

	err = ioutil.WriteFile(output, b, 0666)
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (d *DAG) recurseGraph(top *Node, node *Node, graph *simple.DirectedGraph, last *graphNode, graphNodes map[NodeKey]*graphNode) {
	if top == node {
		panic("unexpected cycle")
	}
	node.Mutex.Lock()
	if node.flagGeneration != d.flagGeneration {
		node.flags = 0
		node.flagGeneration = d.flagGeneration
	}
	if node.flags&Visited != 0 {
		node.Mutex.Unlock()
		return
	}
	node.flags |= Visited
	importCopy := append(make([]Import, 0, len(node.Imports)), node.Imports...)
	node.Mutex.Unlock()

	if gn, ok := graphNodes[node.NodeKey]; ok {
		last = gn
	}
	for _, imported := range importCopy {
		if gn, ok := graphNodes[imported.NodeKey]; ok {
			e := graph.NewEdge(last, gn)
			graph.SetEdge(e)
		}
		d.recurseGraph(top, imported.Node, graph, last, graphNodes)
	}
	return
}
