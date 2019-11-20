package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/davecgh/go-spew/spew"
)

func env(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}

func runCmd(ctx context.Context, dir string, tool string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == context.Canceled {
		return err
	} else if err != nil {
		fmt.Println(strings.Join(append(env, append([]string{tool}, args...)...), " "))
		fmt.Print(string(out))
		return err
	}
	if tool == toolCgo {
		fmt.Println(strings.Join(append(env, append([]string{tool}, args...)...), " "))
		fmt.Print(string(out))
	}
	return nil
}

func why(path string) {
	a := nodeMap[path]
	for _, d := range a.dependants {
		log.Println(d.path)
	}
	spew.Dump(a.pkg)
	panic(path)
}
