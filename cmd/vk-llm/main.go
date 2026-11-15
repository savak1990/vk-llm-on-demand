// Package main is the entrypoint for the vk-llm CLI.
package main

import (
	"os"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr, clock.Real{}))
}
