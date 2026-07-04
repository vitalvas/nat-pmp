package main

import (
	"context"
	"os"

	"github.com/vitalvas/nat-pmp/internal/cli"
)

// version is set by the linker via -ldflags at release time.
var version = "dev"

func main() {
	os.Exit(cli.Execute(context.Background(), version))
}
