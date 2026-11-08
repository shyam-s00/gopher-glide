package mcp

import (
	"io"
	"os"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// pipePair returns a reader for the server and the writer that feeds it.
func pipePair() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

func osArgs0() string { return os.Args[0] }
