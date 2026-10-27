// Package applog writes PowerDeck's log to <data>/logs/powerdeck.log, keeping one previous file.
package applog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const maxSize = 2 << 20

// Open returns a logger writing to the log file (and to extra, if given).
func Open(dataDir string, extra io.Writer) (*slog.Logger, io.Closer, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "powerdeck.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSize {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	if extra != nil {
		w = io.MultiWriter(f, extra)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})), f, nil
}
