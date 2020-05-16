package puller

import (
	"context"
	gobuild "go/build"

	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
)

type Puller struct {
	BuildCtx gobuild.Context
	Tools    build.Tools

	CacheDir string
	WorkDir  string
}

func (c *Puller) Visit(ctx context.Context, node *dag.Node) error {
	return nil
}
