package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func useCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := cacheDir
	cacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheDir = old })
	return dir
}

func TestDiscovery_RoundTripAndOwnerOnlyFile(t *testing.T) {
	cache := useCacheDir(t)
	ws := t.TempDir()
	remove, err := RegisterDaemon(ws, Daemon{URL: "http://127.0.0.1:1/mcp", Token: "tok-0123456789abcdef", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := FindDaemon(ws)
	if !ok || d.URL != "http://127.0.0.1:1/mcp" || d.Token != "tok-0123456789abcdef" || d.PID != os.Getpid() {
		t.Fatalf("found %+v %v", d, ok)
	}

	files, _ := filepath.Glob(filepath.Join(cache, "gg", "daemons", "*.json"))
	if len(files) != 1 {
		t.Fatalf("files = %v; the record must sit in the cache dir, not in the workspace", files)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(files[0])
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	if entries, _ := os.ReadDir(ws); len(entries) != 0 {
		t.Errorf("the workspace must stay clean: %v", entries)
	}

	remove()
	if _, ok := FindDaemon(ws); ok {
		t.Error("record should be gone after remove")
	}
}

func TestDiscovery_DeadProcessIsNotADaemon(t *testing.T) {
	useCacheDir(t)
	ws := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterDaemon(ws, Daemon{URL: "http://127.0.0.1:1/mcp", PID: cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if d, ok := FindDaemon(ws); ok {
		t.Fatalf("a stale record (pid %d is gone) must be ignored: %+v", d.PID, d)
	}
}

func TestDiscovery_WorkspacesAreSeparate(t *testing.T) {
	useCacheDir(t)
	a, b := t.TempDir(), t.TempDir()
	if _, err := RegisterDaemon(a, Daemon{URL: "http://a/mcp", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindDaemon(b); ok {
		t.Error("another workspace must not see the record")
	}
	if d, ok := FindDaemon(filepath.Join(a, "..", filepath.Base(a))); !ok || d.URL != "http://a/mcp" {
		t.Errorf("the same directory spelled differently must match: %+v %v", d, ok)
	}
}

func TestDiscovery_RemoveOnlyDeletesItsOwnRecord(t *testing.T) {
	useCacheDir(t)
	ws := t.TempDir()
	remove, err := RegisterDaemon(ws, Daemon{URL: "http://old/mcp", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	// A newer daemon (another process) for the same workspace replaces the record.
	if _, err := RegisterDaemon(ws, Daemon{URL: "http://newer/mcp", PID: os.Getppid()}); err != nil {
		t.Fatal(err)
	}
	remove()
	if d, ok := FindDaemon(ws); !ok || d.URL != "http://newer/mcp" {
		t.Errorf("an exiting daemon must not delete a successor's record: %+v %v", d, ok)
	}
}

func TestDiscovery_UnreadableRecordIsIgnored(t *testing.T) {
	cache := useCacheDir(t)
	ws := t.TempDir()
	if _, err := RegisterDaemon(ws, Daemon{URL: "http://x/mcp", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(cache, "gg", "daemons", "*.json"))
	if err := os.WriteFile(files[0], []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindDaemon(ws); ok {
		t.Error("a corrupt record must be ignored")
	}
}
