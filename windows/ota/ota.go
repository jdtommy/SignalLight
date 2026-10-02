// Package ota sends a firmware image to the light over BLE. Protocol (see
// docs/FIRMWARE_UPDATES.md and handleOta in the sketch):
//
//   - Control (19B10004, acked writes): "BEGIN <size> <sha256 hex>", "END", "ABORT".
//   - Status (19B10005, notify): "READY", "ACK <seq>", "OK", "ABORTED", "ERR <reason>".
//   - Data (19B10006, write without response): [seq uint32 LE][image bytes].
//
// The light ACKs every AckEvery chunks and we keep at most Window chunks
// unacknowledged. Progress is driven only by the light's ACKs and its final
// SHA-256 check, never by write calls succeeding: WinRT can report a write as
// successful when it never reached the light.
//
// The connection must already be authenticated (AUTH:<secret>); the light refuses
// BEGIN otherwise.
package ota

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"tinygo.org/x/bluetooth"
)

var (
	ControlUUID = mustUUID("19B10004-E8F2-537E-4F6C-D104768A1214")
	StatusUUID  = mustUUID("19B10005-E8F2-537E-4F6C-D104768A1214")
	DataUUID    = mustUUID("19B10006-E8F2-537E-4F6C-D104768A1214")
)

const (
	// Window is the max chunks in flight; AckEvery must match OTA_ACK_EVERY in the
	// sketch. Window 32 sends the firmware in about 10s (throughput results).
	Window   = 32
	AckEvery = 16

	// MaxImageSize is an app slot in the light's partition table (app0/app1, 3MB).
	MaxImageSize = 3 * 1024 * 1024

	// imageMagic is the first byte of every ESP32 app image; the light's Update
	// library rejects anything else.
	imageMagic = 0xE9

	maxChunk = 512 // Data characteristic size on the light
)

// Timeouts, variables so tests can shorten them.
var (
	replyTimeout = 10 * time.Second // BEGIN/END replies
	ackTimeout   = 5 * time.Second  // Light's idle abort is 15s
)

// Characteristic is the subset of bluetooth.DeviceCharacteristic used here.
type Characteristic interface {
	Write(p []byte) (int, error)
	WriteWithoutResponse(p []byte) (int, error)
	EnableNotifications(callback func(buf []byte)) error
	GetMTU() (uint16, error)
}

// DeviceError is an "ERR <reason>" reply from the light.
type DeviceError struct{ Reason string }

func (e *DeviceError) Error() string { return "light reported error: " + e.Reason }

// ValidateImage checks that b looks like a firmware image the light will accept.
func ValidateImage(b []byte) error {
	switch {
	case len(b) == 0:
		return errors.New("firmware file is empty")
	case len(b) > MaxImageSize:
		return fmt.Errorf("firmware is %d bytes; the light's app slot holds %d", len(b), MaxImageSize)
	case b[0] != imageMagic:
		return fmt.Errorf("not an ESP32 app image (first byte 0x%02X, want 0x%02X). Use SignalLight.ino.bin, not the merged or bootloader image", b[0], imageMagic)
	}
	return nil
}

// Find picks the update characteristics out of a discovery result. ok is false
// on firmware that predates Bluetooth updates.
func Find(chars []bluetooth.DeviceCharacteristic) (control, status, data bluetooth.DeviceCharacteristic, ok bool) {
	var found int
	for _, c := range chars {
		switch c.UUID() {
		case ControlUUID:
			control, found = c, found|1
		case StatusUUID:
			status, found = c, found|2
		case DataUUID:
			data, found = c, found|4
		}
	}
	return control, status, data, found == 7
}

// Session sends updates over one authenticated connection.
type Session struct {
	control, data Characteristic
	chunk         int // Bytes per data write, including the 4-byte sequence number

	lastAck   atomic.Int64
	ackSignal chan struct{}
	replies   chan string
}

// NewSession subscribes to the light's status notifications. Call it once per
// connection.
func NewSession(control, status, data Characteristic) (*Session, error) {
	mtu, err := data.GetMTU()
	if err != nil {
		return nil, fmt.Errorf("get MTU: %w", err)
	}
	s := &Session{
		control:   control,
		data:      data,
		chunk:     min(int(mtu)-3, maxChunk), // 3-byte ATT write header
		ackSignal: make(chan struct{}, 1),
		replies:   make(chan string, 16),
	}
	if s.chunk < 5 {
		return nil, fmt.Errorf("MTU %d is too small", mtu)
	}
	s.lastAck.Store(-1)
	if err := status.EnableNotifications(s.onStatus); err != nil {
		return nil, fmt.Errorf("subscribe to update status: %w", err)
	}
	return s, nil
}

func (s *Session) onStatus(b []byte) {
	msg := strings.TrimSpace(string(b))
	if after, ok := strings.CutPrefix(msg, "ACK "); ok {
		if n, err := strconv.ParseInt(after, 10, 64); err == nil {
			s.lastAck.Store(n)
			select {
			case s.ackSignal <- struct{}{}:
			default:
			}
		}
		return
	}
	select {
	case s.replies <- msg:
	default: // Nobody listening; drop rather than block the BLE callback
	}
}

// Send installs image on the light. On success the light reboots into the new
// firmware about a second later; it rolls back unless the app reconnects and
// authenticates within 5 minutes. progress (may be nil) gets the bytes the light
// has acknowledged so far.
func (s *Session) Send(image []byte, progress func(done, total int)) (err error) {
	if err := ValidateImage(image); err != nil {
		return err
	}
	sum := sha256.Sum256(image)

	for len(s.replies) > 0 { // Stale replies from an earlier attempt
		<-s.replies
	}
	s.lastAck.Store(-1)

	if err := s.command(fmt.Sprintf("BEGIN %d %s", len(image), hex.EncodeToString(sum[:]))); err != nil {
		return err
	}
	if err := s.expect("READY"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = s.command("ABORT") // Best effort; the light also aborts on disconnect or after 15s idle
		}
	}()

	perChunk := s.chunk - 4
	chunks := (len(image) + perChunk - 1) / perChunk
	report := func() {
		if progress != nil {
			acked := int(s.lastAck.Load()+1) * perChunk
			progress(min(acked, len(image)), len(image))
		}
	}

	buf := make([]byte, s.chunk)
	for seq := 0; seq < chunks; seq++ {
		if err := s.waitAck(int64(seq-Window), report); err != nil {
			return fmt.Errorf("at chunk %d of %d: %w", seq, chunks, err)
		}
		off := seq * perChunk
		binary.LittleEndian.PutUint32(buf, uint32(seq))
		n := 4 + copy(buf[4:], image[off:min(off+perChunk, len(image))])
		if _, err := s.data.WriteWithoutResponse(buf[:n]); err != nil {
			return fmt.Errorf("write chunk %d: %w", seq, err)
		}
	}
	// The light ACKs the final chunk even off the AckEvery boundary.
	if err := s.waitAck(int64(chunks-1), report); err != nil {
		return fmt.Errorf("waiting for the light to receive the last chunk: %w", err)
	}

	if err := s.command("END"); err != nil {
		return err
	}
	return s.expect("OK")
}

// waitAck blocks until the light has acknowledged chunk seq, failing fast on an
// error reply.
func (s *Session) waitAck(seq int64, report func()) error {
	for s.lastAck.Load() < seq {
		select {
		case <-s.ackSignal:
			report()
		case msg := <-s.replies:
			return replyError(msg)
		case <-time.After(ackTimeout):
			return fmt.Errorf("no acknowledgement from the light for %v (last ACK %d)", ackTimeout, s.lastAck.Load())
		}
	}
	return nil
}

func (s *Session) command(cmd string) error {
	if _, err := s.control.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("send %s: %w", strings.Fields(cmd)[0], err)
	}
	return nil
}

func (s *Session) expect(want string) error {
	select {
	case msg := <-s.replies:
		if msg == want {
			return nil
		}
		return replyError(msg)
	case <-time.After(replyTimeout):
		return fmt.Errorf("no reply from the light within %v (waiting for %s)", replyTimeout, want)
	}
}

func replyError(msg string) error {
	if reason, ok := strings.CutPrefix(msg, "ERR "); ok {
		return &DeviceError{Reason: reason}
	}
	return fmt.Errorf("unexpected reply from the light: %q", msg)
}

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}
