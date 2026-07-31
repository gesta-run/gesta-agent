package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gesta-run/gesta-agent/pkg/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.Run(ctx, os.Args[1:]); err != nil {
		var exitErr agent.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.Message != "" {
				log.Print(exitErr.Message)
			}
			os.Exit(exitErr.Code)
		}
		log.Fatal(err)
	}
}
