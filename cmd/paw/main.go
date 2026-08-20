package main

import (
	"os"

	"git.alc.xyz/alcxyz/paw/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
