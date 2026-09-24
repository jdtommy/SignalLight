package server

import (
	"fmt"
	"net"
	"testing"

	"signallight/config"
)

func TestResolveListener_ExplicitPort(t *testing.T) {
	// Pick an open port first to test with
	tempListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind temp listener: %v", err)
	}
	targetPort := tempListener.Addr().(*net.TCPAddr).Port
	tempListener.Close()

	cfg := &config.Config{}
	l, port, err := ResolveListener(fmt.Sprintf("%d", targetPort), cfg)
	if err != nil {
		t.Fatalf("ResolveListener with explicit port failed: %v", err)
	}
	defer l.Close()

	if port != targetPort {
		t.Errorf("expected port %d, got %d", targetPort, port)
	}
	if cfg.WebPort != targetPort {
		t.Errorf("expected cfg.WebPort %d, got %d", targetPort, cfg.WebPort)
	}
}

func TestResolveListener_AutoPort(t *testing.T) {
	cfg := &config.Config{}
	l, port, err := ResolveListener("", cfg)
	if err != nil {
		t.Fatalf("ResolveListener auto failed: %v", err)
	}
	defer l.Close()

	if port <= 0 || port > 65535 {
		t.Errorf("expected valid port, got %d", port)
	}
	if cfg.WebPort != port {
		t.Errorf("expected cfg.WebPort to be updated to %d, got %d", port, cfg.WebPort)
	}
}

func TestResolveListener_SavedPortBusy(t *testing.T) {
	// Hold an occupied port
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind blocker: %v", err)
	}
	defer blocker.Close()
	busyPort := blocker.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		WebPort: busyPort,
	}

	l, port, err := ResolveListener("", cfg)
	if err != nil {
		t.Fatalf("ResolveListener with busy saved port failed: %v", err)
	}
	defer l.Close()

	if port == busyPort {
		t.Errorf("expected port different from busyPort %d, got %d", busyPort, port)
	}
	if cfg.WebPort == busyPort {
		t.Errorf("expected cfg.WebPort to be updated away from busyPort %d", busyPort)
	}
}
