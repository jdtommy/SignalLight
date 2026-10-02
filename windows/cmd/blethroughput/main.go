// Command blethroughput measures BLE transfer speed to the BleThroughputTest
// sketch (arduino/BleThroughputTest). It's step 1 of the firmware update plan in
// docs/FIRMWARE_UPDATES.md: find out whether ArduinoBLE is fast enough to send a
// firmware image. Quit SignalLight first; the light accepts only one connection
// at a time.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-ole/go-ole"
	"tinygo.org/x/bluetooth"
)

var (
	serviceUUID = mustUUID("19B10100-E8F2-537E-4F6C-D104768A1214")
	dataUUID    = mustUUID("19B10101-E8F2-537E-4F6C-D104768A1214")
	ctrlUUID    = mustUUID("19B10102-E8F2-537E-4F6C-D104768A1214")
)

// Size of the current SignalLight firmware image (arduino-cli compile, Oct 2026),
// used for the estimate column.
const firmwareImageSize = 469 * 1024

type mode struct {
	noResponse bool // WriteWithoutResponse, paced by the device's ACKs
	window     int  // Max chunks in flight (noResponse only)
	flash      bool // Device writes the data to its spare OTA slot
}

func (m mode) String() string {
	s := "acked writes"
	if m.noResponse {
		s = fmt.Sprintf("no-response, window %d", m.window)
	}
	if m.flash {
		s += " + flash"
	}
	return s
}

type link struct {
	data, ctrl bluetooth.DeviceCharacteristic
	chunk      int // Total bytes per data write (4-byte sequence number + payload)
	notes      chan string
	lastAck    atomic.Int64
	ackSignal  chan struct{}
}

func main() {
	size := flag.Int("size", 128*1024, "bytes to send per run")
	windows := flag.String("windows", "4,8,16,32", "comma-separated ACK window sizes to test")
	withFlash := flag.Bool("flash", true, "also repeat every mode with the device writing to flash")
	flag.Parse()

	// WinRT calls must stay on one COM-initialized OS thread. adapter.Enable()
	// (in connect) does the COM init; initializing here first makes it fail.
	runtime.LockOSThread()
	defer ole.CoUninitialize()

	l, dev, err := connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer dev.Disconnect()

	var modes []mode
	for _, f := range []bool{false, true} {
		if f && !*withFlash {
			continue
		}
		modes = append(modes, mode{flash: f})
		for _, w := range strings.Split(*windows, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(w))
			if err != nil || n < 2 {
				fmt.Fprintf(os.Stderr, "error: bad window %q (must be a number >= 2)\n", w)
				os.Exit(1)
			}
			modes = append(modes, mode{noResponse: true, window: n, flash: f})
		}
	}

	fmt.Printf("\n%-28s %9s %15s %6s %9s\n", "Mode", "KB/s", "Est. firmware", "Gaps", "Checksum")
	for _, m := range modes {
		res, err := l.run(m, *size)
		if err != nil {
			fmt.Printf("%-28s FAILED: %v\n", m, err)
			continue
		}
		fmt.Printf("%-28s %9.1f %15s %6d %9s\n", m, res.kbps, res.estimate.Round(time.Second), res.gaps, res.checksum)
	}
}

func connect() (*link, bluetooth.Device, error) {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return nil, bluetooth.Device{}, fmt.Errorf("enable adapter: %w", err)
	}

	fmt.Println("Scanning for SL-ThroughputTest (10s)...")
	var found *bluetooth.ScanResult
	stop := time.AfterFunc(10*time.Second, func() { _ = adapter.StopScan() })
	err := adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if r.LocalName() == "SL-ThroughputTest" {
			found = &r
			_ = a.StopScan()
		}
	})
	stop.Stop()
	if err != nil {
		return nil, bluetooth.Device{}, fmt.Errorf("scan: %w", err)
	}
	if found == nil {
		return nil, bluetooth.Device{}, errors.New("SL-ThroughputTest not found. Is the test sketch flashed, and is SignalLight closed?")
	}

	fmt.Printf("Connecting to %s...\n", found.Address.String())
	dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, bluetooth.Device{}, fmt.Errorf("connect: %w", err)
	}
	time.Sleep(time.Second) // Same WinRT settling delay the app uses

	svcs, err := dev.DiscoverServices([]bluetooth.UUID{serviceUUID})
	if err != nil || len(svcs) == 0 {
		dev.Disconnect()
		return nil, bluetooth.Device{}, fmt.Errorf("discover service: %v", err)
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{dataUUID, ctrlUUID})
	if err != nil {
		dev.Disconnect()
		return nil, bluetooth.Device{}, fmt.Errorf("discover characteristics: %w", err)
	}

	l := &link{notes: make(chan string, 64), ackSignal: make(chan struct{}, 1)}
	for _, c := range chars {
		switch c.UUID() {
		case dataUUID:
			l.data = c
		case ctrlUUID:
			l.ctrl = c
		}
	}

	mtu, err := l.data.GetMTU()
	if err != nil {
		dev.Disconnect()
		return nil, bluetooth.Device{}, fmt.Errorf("get MTU: %w", err)
	}
	l.chunk = min(int(mtu)-3, 512) // 3-byte ATT write header; 512 = characteristic size on the device
	fmt.Printf("Negotiated MTU %d: %d bytes per write (%d of data after the 4-byte sequence number)\n",
		mtu, l.chunk, l.chunk-4)

	err = l.ctrl.EnableNotifications(func(b []byte) {
		s := strings.TrimSpace(string(b))
		// ArduinoBLE notifies subscribers of the value a central writes, so our
		// own START commands come back as notifications. Not a device reply.
		if strings.HasPrefix(s, "START ") {
			return
		}
		if after, ok := strings.CutPrefix(s, "ACK "); ok {
			if n, err := strconv.ParseInt(after, 10, 64); err == nil {
				l.lastAck.Store(n)
				select {
				case l.ackSignal <- struct{}{}:
				default:
				}
			}
			return
		}
		l.notes <- s
	})
	if err != nil {
		dev.Disconnect()
		return nil, bluetooth.Device{}, fmt.Errorf("enable notifications: %w", err)
	}
	return l, dev, nil
}

type result struct {
	kbps     float64
	estimate time.Duration
	gaps     int
	checksum string
}

func (l *link) run(m mode, size int) (result, error) {
	for len(l.notes) > 0 {
		<-l.notes
	}
	l.lastAck.Store(-1)

	payload := make([]byte, size)
	rand.Read(payload)
	payload[0] = 0xE9 // The ESP32 Update library rejects data that doesn't start with the image magic byte
	want := fnv1a(payload)

	ackEvery := 0
	if m.noResponse {
		ackEvery = max(1, m.window/2)
	}
	flash := 0
	if m.flash {
		flash = 1
	}
	if _, err := l.ctrl.Write([]byte(fmt.Sprintf("START %d %d %d", size, ackEvery, flash))); err != nil {
		return result{}, fmt.Errorf("send START: %w", err)
	}
	if note, err := l.waitNote(5 * time.Second); err != nil {
		return result{}, err
	} else if note != "READY" {
		return result{}, fmt.Errorf("device replied %q", note)
	}

	perChunk := l.chunk - 4
	buf := make([]byte, l.chunk)
	start := time.Now()
	for seq, off := 0, 0; off < size; seq, off = seq+1, off+perChunk {
		end := min(off+perChunk, size)
		binary.LittleEndian.PutUint32(buf, uint32(seq))
		n := 4 + copy(buf[4:], payload[off:end])

		var err error
		if m.noResponse {
			for int64(seq)-l.lastAck.Load() > int64(m.window) {
				select {
				case <-l.ackSignal:
				case <-time.After(5 * time.Second):
					return result{}, fmt.Errorf("no ACK for 5s at chunk %d (last ACK %d)", seq, l.lastAck.Load())
				}
			}
			_, err = l.data.WriteWithoutResponse(buf[:n])
		} else {
			_, err = l.data.Write(buf[:n])
		}
		if err != nil {
			return result{}, fmt.Errorf("write chunk %d: %w", seq, err)
		}
	}

	note, err := l.waitNote(30 * time.Second)
	if err != nil {
		return result{}, err
	}
	elapsed := time.Since(start)
	var bytes, ms, gaps, maxChunk, flashErrors int
	var hash uint32
	if _, err := fmt.Sscanf(note, "DONE %d %d %x %d %d %d", &bytes, &ms, &hash, &gaps, &maxChunk, &flashErrors); err != nil {
		return result{}, fmt.Errorf("unexpected device reply %q", note)
	}
	if bytes != size {
		return result{}, fmt.Errorf("device received %d of %d bytes", bytes, size)
	}
	if flashErrors > 0 {
		return result{}, fmt.Errorf("device reported %d flash write errors", flashErrors)
	}

	checksum := "ok"
	if hash != want {
		checksum = "MISMATCH"
	}
	kbps := float64(size) / 1024 / elapsed.Seconds()
	return result{
		kbps:     kbps,
		estimate: time.Duration(float64(firmwareImageSize) / 1024 / kbps * float64(time.Second)),
		gaps:     gaps,
		checksum: checksum,
	}, nil
}

func (l *link) waitNote(timeout time.Duration) (string, error) {
	select {
	case s := <-l.notes:
		return s, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("no reply from device within %v", timeout)
	}
}

func fnv1a(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}
