package main

import (
	"os"

	"pixelsup-go/internal/cli"
)

// main is the process entry point for the Go CLI migration track.
// It delegates argument parsing and command dispatch to internal/cli.
func main() {
	root := cli.BuildRootCommand()
	code := root.Execute(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
