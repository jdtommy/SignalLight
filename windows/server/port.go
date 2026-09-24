package server

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	"signallight/config"
)

// ResolveListener binds a TCP listener to a designated or automatically chosen open port on 127.0.0.1.
// Selection priority:
// 1. Explicit port requested via CLI flag (e.g. "-port 9911")
// 2. Persisted web_port from config.json (if set and currently available)
// 3. Automatically chosen open port from friendly range (9120-9900)
// 4. OS-assigned ephemeral open port (port 0)
//
// The chosen port is saved to config.json so it remains stable across runs.
func ResolveListener(explicitPort string, cfg *config.Config) (net.Listener, int, error) {
	explicitPort = strings.TrimSpace(explicitPort)
	explicitPort = strings.TrimPrefix(explicitPort, ":")

	// 1. Explicit flag provided by user
	if explicitPort != "" {
		p, err := strconv.Atoi(explicitPort)
		if err != nil || p <= 0 || p > 65535 {
			return nil, 0, fmt.Errorf("invalid port '%s': must be between 1 and 65535", explicitPort)
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to bind requested port %d: %w", p, err)
		}
		if cfg != nil && cfg.WebPort != p {
			cfg.WebPort = p
			_ = config.Save(cfg)
		}
		return l, p, nil
	}

	// 2. Try previously saved port from config.json
	if cfg != nil && cfg.WebPort > 0 && cfg.WebPort <= 65535 {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.WebPort))
		if err == nil {
			return l, cfg.WebPort, nil
		}
		log.Printf("[Web] Saved port %d is unavailable (%v), finding a new open port...", cfg.WebPort, err)
	}

	// 3. Search for an open port in a friendly range (9120 - 9900)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	const minPort = 9120
	const maxPort = 9900
	for attempt := 0; attempt < 25; attempt++ {
		candidate := minPort + rng.Intn(maxPort-minPort+1)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", candidate))
		if err == nil {
			if cfg != nil {
				cfg.WebPort = candidate
				_ = config.Save(cfg)
			}
			return l, candidate, nil
		}
	}

	// 4. Fallback to OS dynamic ephemeral port allocation (port 0)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to allocate any open port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if cfg != nil {
		cfg.WebPort = port
		_ = config.Save(cfg)
	}
	return l, port, nil
}
