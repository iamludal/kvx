package main

import (
	"os"

	"github.com/iamludal/kvx/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
