// Package applog sends the standard logger to a file. The installed build has no
// console window (it's linked with -H=windowsgui so starting at login doesn't pop
// one up), so without this its logs would go nowhere.
package applog

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

const FileName = "signallight.log"

// Init appends log output to dir/signallight.log and, when a console exists, to
// stderr as well. Once the file grows past maxSize it's moved to
// signallight.log.old at startup, so at most two files' worth is kept.
func Init(dir string, maxSize int64) (*os.File, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSize {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	// File first: in a GUI build stderr is invalid and its write fails, and
	// io.MultiWriter stops at the first failing writer.
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	return f, nil
}
