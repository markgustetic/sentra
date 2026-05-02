package main

import (
	"os"

	"github.com/markgustetic/sentra/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.NewRoot(version, commit, date).Execute(); err != nil {
		os.Exit(1)
	}
}
