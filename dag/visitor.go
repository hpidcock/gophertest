package dag

import (
	"context"
)

type Visitor interface {
	Visit(context.Context, *Node, VisitDirection) error
}

type VisitorFunc func(context.Context, *Node, VisitDirection) error

func (f VisitorFunc) Visit(ctx context.Context, n *Node, direction VisitDirection) error {
	return f(ctx, n, direction)
}

type VisitDirection int

const (
	Left  = 0
	Right = 1
)
