package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dyne/lsget/internal/lsget"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := lsget.RunCLI(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
