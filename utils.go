package main

import (
	"os"
)

func env(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}
