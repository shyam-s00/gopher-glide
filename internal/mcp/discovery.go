package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Daemon is how a running `gg mcp --port` daemon is found again.
type Daemon struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
	PID   int    `json:"pid"`
}

// cacheDir is where discovery files live; tests replace it.
var cacheDir = os.UserCacheDir

// discoveryPath is the file for a workspace, outside the workspace itself so
// it can never be committed.
func discoveryPath(workspace string) (string, error) {
	base, err := cacheDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(base, "gg", "daemons", hex.EncodeToString(sum[:8])+".json"), nil
}

// RegisterDaemon records d as the daemon for workspace and returns a function
// that removes the record, but only while it is still this daemon's.
func RegisterDaemon(workspace string, d Daemon) (remove func(), err error) {
	path, err := discoveryPath(workspace)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*")
	if err != nil {
		return nil, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return nil, werr
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("write discovery file: %w", err)
	}
	return func() {
		if cur, ok := readDaemon(path); ok && cur.PID == d.PID {
			_ = os.Remove(path)
		}
	}, nil
}

// FindDaemon returns the live daemon registered for workspace. A record whose
// process is gone, or that cannot be read, is not a daemon.
func FindDaemon(workspace string) (Daemon, bool) {
	path, err := discoveryPath(workspace)
	if err != nil {
		return Daemon{}, false
	}
	d, ok := readDaemon(path)
	if !ok || d.URL == "" || !processAlive(d.PID) {
		return Daemon{}, false
	}
	return d, true
}

func readDaemon(path string) (Daemon, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Daemon{}, false
	}
	var d Daemon
	if json.Unmarshal(b, &d) != nil {
		return Daemon{}, false
	}
	return d, true
}
