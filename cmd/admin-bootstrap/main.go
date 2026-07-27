package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// bootstrapCommandStore is intentionally left nil until the database adapter
// is wired by the storage task. The command fails closed without reading a
// password rather than running with a non-transactional placeholder.
var bootstrapCommandStore bootstrapStore

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, bootstrapCommandStore); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "admin bootstrap failed: %v\n", err)
		os.Exit(1)
	}
}
