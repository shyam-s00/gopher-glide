package main

import (
	"fmt"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/tui"
	"github.com/shyam-s00/gopher-glide/internal/version"
	"os"
)

func main() {
	fmt.Printf("gg (Gopher Glide) %s (commit:%s) built %s\n", version.Version, version.GitCommit, version.GetBuildDate())

	if len(os.Args) < 2 {
		fmt.Println("Usage: gg <config-file>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	cfg, err := config.Load(configPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Configuration loaded successfully\n")
	fmt.Printf("  HttpFile: %s\n", cfg.ConfigSection.HTTPFilePath)
	fmt.Printf("  Prometheus: %t\n", cfg.ConfigSection.Prometheus)
	fmt.Printf("  Stages: %d stage(s)\n", len(cfg.Stages))
	for i, s := range cfg.Stages {
		fmt.Printf("    [%d] duration=%s targetRPS=%d\n", i+1, s.Duration, s.TargetRPS)
	}

	// Parse the .http file — resolved to the same directory as config.yaml
	specs, err := httpreader.ParseFile(cfg.ConfigSection.HTTPFilePath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error parsing http file: %v\n", err)
		os.Exit(1)
	}
	if len(specs) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "No requests found in http file: %s\n", cfg.ConfigSection.HTTPFile)
		os.Exit(1)
	}

	fmt.Printf("✓ Loaded %d request(s) from %s\n", len(specs), cfg.ConfigSection.HTTPFile)
	for i, s := range specs {
		fmt.Printf("  [%d] %s %s\n", i+1, s.Method, s.URL)
	}

	eng := engine.New()

	fmt.Println("Starting TUI...")
	if err := tui.Start(eng, cfg, specs); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
