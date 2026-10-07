package main

import (
	"os"

	"pixelsup-go/internal/cli"
)

// main delegates argument parsing and command dispatch to internal/cli.
func main() {
	root := cli.BuildRootCommand()
	code := root.Execute(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
