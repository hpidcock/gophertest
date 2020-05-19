package dag

import (
	"context"
)

type Visitor interface {
	Visit(context.Context, *Node) error
}

type VisitorFunc func(context.Context, *Node) error

func (f VisitorFunc) Visit(ctx context.Context, n *Node) error {
	return f(ctx, n)
}

type VisitDirection int

const (
	Left  = 0
	Right = 1
)
