package main

import (
	"context"
	"os"

	"go.vitalvas.com/nat-pmp/internal/cli"
)

// version is set by the linker via -ldflags at release time.
var version = "dev"

func main() {
	os.Exit(cli.Execute(context.Background(), version))
}
