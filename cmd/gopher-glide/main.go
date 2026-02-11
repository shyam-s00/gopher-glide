package main

import (
	"fmt"
	"gopher-glide/internal/config"
	"os"
)

func main() {
	fmt.Println("Gopher-Glide - Api load testing tool using Go")

	if len(os.Args) < 2 {
		fmt.Println("Usage: gopher-glide <config-file>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	cfg, err := config.Load(configPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Configuration loaded successfully\n")
	fmt.Printf(" HttpFile: %s\n", cfg.ConfigSection.HTTPFile)
	fmt.Printf(" Prometheus: %t\n", cfg.ConfigSection.Prometheus)
	fmt.Printf(" Stages: %v\n", cfg.Stages)
}
