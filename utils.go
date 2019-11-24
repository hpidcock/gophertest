package main

import (
	"log"
	"os"

	"github.com/davecgh/go-spew/spew"
)

func env(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}

func why(path string) {
	a := nodeMap[path]
	for _, d := range a.dependants {
		log.Println(d.path)
	}
	spew.Dump(a.pkg)
	panic(path)
}
