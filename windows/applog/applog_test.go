package applog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesToFileAndRotatesWhenTooLarge(t *testing.T) {
	dir := t.TempDir()
	orig := log.Writer()
	t.Cleanup(func() { log.SetOutput(orig) })

	// An oversized existing log should be moved aside to .old.
	big := filepath.Join(dir, FileName)
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 200)), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := Init(dir, 100)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	log.Print("hello from the test")
	f.Close()

	old, err := os.ReadFile(big + ".old")
	if err != nil || len(old) != 200 {
		t.Errorf("expected the oversized log rotated to .old (len 200), got len %d, err %v", len(old), err)
	}
	cur, err := os.ReadFile(big)
	if err != nil || !strings.Contains(string(cur), "hello from the test") {
		t.Errorf("expected new log to contain the message, got %q, err %v", cur, err)
	}
}
