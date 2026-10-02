package server

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates every test in this package from the real config. Code under
// test (ResolveListener, the pair/unpair handlers) calls config.Save/Clear, which
// write ./config.json or %APPDATA%\SignalLight\config.json; running the tests once
// wiped a real light's pairing that way.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "signallight-server-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("APPDATA", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
