// Command otaflash installs a firmware image on the paired light over Bluetooth,
// using the light's saved pairing from SignalLight's config.json. It's a test tool
// for the update protocol (step 4 of docs/FIRMWARE_UPDATES.md); the dashboard will
// do this for users. Quit SignalLight first: the light accepts only one
// connection at a time.
//
//	go run ./cmd/otaflash path\to\SignalLight.ino.bin
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/go-ole/go-ole"
	"tinygo.org/x/bluetooth"

	"signallight/ble"
	"signallight/config"
	"signallight/ota"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: otaflash <firmware.bin>")
		fmt.Fprintln(os.Stderr, "Installs firmware on the paired light over Bluetooth. Quit SignalLight first.")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	image, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := ota.ValidateImage(image); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("read %s: %w", config.GetConfigPath(), err)
	}
	if !cfg.Paired || cfg.TargetMAC == "" || cfg.SharedSecret == "" {
		return errors.New("no paired light in " + config.GetConfigPath() + "; pair one in the dashboard first")
	}
	fmt.Printf("Firmware: %s (%d bytes, sha256 %x)\n", path, len(image), sha256.Sum256(image))

	// WinRT calls must stay on one COM-initialized OS thread. adapter.Enable()
	// does the COM init; initializing here first makes it fail.
	runtime.LockOSThread()
	defer ole.CoUninitialize()
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable Bluetooth: %w", err)
	}

	c, err := connect(adapter, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Light is running firmware %s\n", c.version())

	control, status, data, ok := ota.Find(c.chars)
	if !ok {
		c.dev.Disconnect()
		return errors.New("this light's firmware doesn't support Bluetooth updates yet; flash it over USB once")
	}
	sess, err := ota.NewSession(control, status, data)
	if err != nil {
		c.dev.Disconnect()
		return err
	}

	start := time.Now()
	err = sess.Send(image, func(done, total int) {
		fmt.Printf("\rSending: %3d%% (%d / %d bytes)", done*100/total, done, total)
	})
	fmt.Println()
	c.dev.Disconnect()
	if err != nil {
		return err
	}
	fmt.Printf("Light verified and installed the firmware in %v. It's rebooting.\n", time.Since(start).Round(100*time.Millisecond))

	// The new firmware rolls itself back unless an authenticated connection
	// confirms it within 5 minutes; reconnecting here does that.
	fmt.Println("Reconnecting to confirm the new firmware...")
	time.Sleep(5 * time.Second)
	deadline := time.Now().Add(60 * time.Second)
	for {
		c, err = connect(adapter, cfg)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("couldn't reconnect after the update (%w). Start SignalLight within 5 minutes or the light rolls back to its previous firmware", err)
		}
		fmt.Println("  not yet:", err)
		time.Sleep(3 * time.Second)
	}
	fmt.Printf("Confirmed. Light is now running firmware %s\n", c.version())
	c.dev.Disconnect()
	return nil
}

type conn struct {
	dev   bluetooth.Device
	chars []bluetooth.DeviceCharacteristic
}

// connect finds the paired light, connects, and authenticates.
func connect(adapter *bluetooth.Adapter, cfg *config.Config) (*conn, error) {
	fmt.Printf("Scanning for %s (%s)...\n", cfg.DeviceName, cfg.TargetMAC)
	var found *bluetooth.ScanResult
	stop := time.AfterFunc(10*time.Second, func() { _ = adapter.StopScan() })
	err := adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if strings.EqualFold(r.Address.String(), cfg.TargetMAC) {
			found = &r
			_ = a.StopScan()
		}
	})
	stop.Stop()
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	if found == nil {
		return nil, errors.New("light not found. Is it powered, and is SignalLight closed?")
	}

	dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	c := &conn{dev: dev}
	if err := c.setup(cfg.SharedSecret); err != nil {
		dev.Disconnect()
		return nil, err
	}
	return c, nil
}

func (c *conn) setup(secret string) error {
	time.Sleep(time.Second) // Same WinRT settling delay the app uses
	svcs, err := c.dev.DiscoverServices([]bluetooth.UUID{ble.ServiceUUID})
	if err != nil || len(svcs) == 0 {
		return fmt.Errorf("discover service: %v", err)
	}
	// Unfiltered: a filtered discovery fails if any UUID is missing.
	c.chars, err = svcs[0].DiscoverCharacteristics(nil)
	if err != nil {
		return fmt.Errorf("discover characteristics: %w", err)
	}
	auth, ok := c.find(ble.AuthCharacteristicUUID)
	if !ok {
		return errors.New("auth characteristic not found")
	}

	// The light must see AUTH within 4s of connecting.
	if _, err := auth.Write([]byte("AUTH:" + secret)); err != nil {
		return fmt.Errorf("send AUTH: %w", err)
	}
	// The value reads back our own AUTH command until the light replies.
	buf := make([]byte, 64)
	for i := 0; i < 20; i++ {
		time.Sleep(150 * time.Millisecond)
		n, err := auth.Read(buf)
		if err != nil {
			return fmt.Errorf("read auth reply: %w", err)
		}
		switch reply := buf[:n]; {
		case bytes.Contains(reply, []byte("AUTH_OK")):
			return nil
		case bytes.Contains(reply, []byte("AUTH_FAIL")), bytes.Contains(reply, []byte("PAIRED")):
			return fmt.Errorf("light rejected authentication (%q); re-pair it in the dashboard", reply)
		}
	}
	return errors.New("no authentication reply from the light")
}

func (c *conn) find(u bluetooth.UUID) (bluetooth.DeviceCharacteristic, bool) {
	for _, ch := range c.chars {
		if ch.UUID() == u {
			return ch, true
		}
	}
	return bluetooth.DeviceCharacteristic{}, false
}

func (c *conn) version() string {
	ch, ok := c.find(ble.VersionCharacteristicUUID)
	if !ok {
		return "(unknown: predates version reporting)"
	}
	buf := make([]byte, 32)
	n, err := ch.Read(buf)
	if err != nil {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	return strings.TrimSpace(string(buf[:n]))
}
