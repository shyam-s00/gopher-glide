package session

import (
	"os"
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
)

// TestMain re-runs the test binary as a scripted fake gg when fakegg.Env is set.
func TestMain(m *testing.M) {
	if sc := os.Getenv(fakegg.Env); sc != "" {
		fakegg.Run(sc)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeSpec launches this test binary as the fake child.
func fakeSpec(t *testing.T, scenario string) Spec {
	t.Helper()
	return Spec{Exe: os.Args[0], Args: []string{"-test.run=^$"}, Env: []string{fakegg.Env + "=" + scenario}, Dir: t.TempDir()}
}
