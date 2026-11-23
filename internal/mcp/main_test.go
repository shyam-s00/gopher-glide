package mcp

import (
	"os"
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
)

// TestMain lets tests launch this binary as a scripted fake gg.
func TestMain(m *testing.M) {
	if sc := os.Getenv(fakegg.Env); sc != "" {
		fakegg.Run(sc)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
