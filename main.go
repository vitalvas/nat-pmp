package main

import (
	"context"
	"os"

	"github.com/vitalvas/nat-pmp/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background()))
}
