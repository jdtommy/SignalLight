package ota

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLight mimics the sketch's receiver (onOtaControlWritten, onOtaDataWritten).
// Notifications go out on their own goroutine, like the real light's, which sends
// them from loop() after the write has returned.
type fakeLight struct {
	mtu uint16

	mu       sync.Mutex
	notify   func([]byte)
	active   bool
	size     int
	sha      string
	nextSeq  uint32
	got      bytes.Buffer
	dropSeq  int // Silently drop this chunk (as a false WinRT write success would); -1 = none
	failSeq  int // Reply ERR WRITE on this chunk; -1 = none
	silent   bool
	queue    chan string
	commands []string
}

func newFakeLight() *fakeLight {
	l := &fakeLight{mtu: 242, dropSeq: -1, failSeq: -1, queue: make(chan string, 1024)}
	go func() {
		for msg := range l.queue {
			l.mu.Lock()
			n := l.notify
			l.mu.Unlock()
			if n != nil {
				n([]byte(msg))
			}
		}
	}()
	return l
}

func (l *fakeLight) send(msg string) {
	if !l.silent {
		l.queue <- msg
	}
}

type ctrlChar struct{ *fakeLight }
type statusChar struct{ *fakeLight }
type dataChar struct{ *fakeLight }

func (l *fakeLight) GetMTU() (uint16, error)                  { return l.mtu, nil }
func (l *fakeLight) Write([]byte) (int, error)                { return 0, errors.New("not writable") }
func (l *fakeLight) WriteWithoutResponse([]byte) (int, error) { return 0, errors.New("not writable") }
func (l *fakeLight) EnableNotifications(func([]byte)) error   { return errors.New("no notify") }
func (c statusChar) EnableNotifications(cb func([]byte)) error {
	c.mu.Lock()
	c.notify = cb
	c.mu.Unlock()
	return nil
}
func (c ctrlChar) Write(p []byte) (int, error)                { c.control(string(p)); return len(p), nil }
func (c dataChar) WriteWithoutResponse(p []byte) (int, error) { c.chunk(p); return len(p), nil }

func (l *fakeLight) control(cmd string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.commands = append(l.commands, cmd)
	switch {
	case strings.HasPrefix(cmd, "BEGIN "):
		if _, err := fmt.Sscanf(cmd, "BEGIN %d %s", &l.size, &l.sha); err != nil {
			l.send("ERR BAD_BEGIN")
			return
		}
		l.active, l.nextSeq = true, 0
		l.got.Reset()
		l.send("READY")
	case cmd == "END":
		sum := sha256.Sum256(l.got.Bytes())
		switch {
		case !l.active:
			l.send("ERR NOT_ACTIVE")
		case l.got.Len() != l.size:
			l.send(fmt.Sprintf("ERR SIZE %d/%d", l.got.Len(), l.size))
		case hex.EncodeToString(sum[:]) != l.sha:
			l.send("ERR SHA_MISMATCH")
		default:
			l.send("OK")
		}
		l.active = false
	case cmd == "ABORT":
		l.active = false
		l.send("ABORTED")
	}
}

func (l *fakeLight) chunk(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := binary.LittleEndian.Uint32(p)
	if !l.active || int(seq) == l.dropSeq {
		return
	}
	if int(seq) == l.failSeq {
		l.active = false
		l.send("ERR WRITE Flash Write Failed")
		return
	}
	if seq != l.nextSeq {
		l.active = false
		l.send(fmt.Sprintf("ERR SEQ expected %d got %d", l.nextSeq, seq))
		return
	}
	l.got.Write(p[4:])
	l.nextSeq++
	if l.nextSeq%AckEvery == 0 || l.got.Len() == l.size {
		l.send(fmt.Sprintf("ACK %d", seq))
	}
}

func testImage(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	b[0] = imageMagic
	return b
}

func newTestSession(t *testing.T, l *fakeLight) *Session {
	t.Helper()
	s, err := NewSession(ctrlChar{l}, statusChar{l}, dataChar{l})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func shortTimeouts(t *testing.T) {
	r, a := replyTimeout, ackTimeout
	replyTimeout, ackTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { replyTimeout, ackTimeout = r, a })
}

func TestSendDeliversImage(t *testing.T) {
	// Sizes around chunk and ACK boundaries: 235 data bytes per chunk at MTU 242.
	for _, size := range []int{1, 235, 236, 235 * AckEvery, 235*AckEvery + 1, 235 * Window * 3, 469 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			l := newFakeLight()
			img := testImage(size)
			var last int
			err := newTestSession(t, l).Send(img, func(done, total int) {
				if done < last || done > total || total != size {
					t.Errorf("bad progress %d/%d after %d", done, total, last)
				}
				last = done
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(l.got.Bytes(), img) {
				t.Fatal("light received different bytes")
			}
			if last != size {
				t.Errorf("final progress %d, want %d", last, size)
			}
		})
	}
}

func TestSendFailsOnLostChunk(t *testing.T) {
	shortTimeouts(t)
	l := newFakeLight()
	l.dropSeq = 40
	err := newTestSession(t, l).Send(testImage(235*100), nil)
	var de *DeviceError
	if !errors.As(err, &de) || !strings.HasPrefix(de.Reason, "SEQ expected 40 got 41") {
		t.Fatalf("got %v, want SEQ error", err)
	}
	if got := l.commands[len(l.commands)-1]; got != "ABORT" {
		t.Errorf("last command %q, want ABORT", got)
	}
}

func TestSendFailsOnLightError(t *testing.T) {
	l := newFakeLight()
	l.failSeq = 3
	err := newTestSession(t, l).Send(testImage(235*100), nil)
	var de *DeviceError
	if !errors.As(err, &de) || de.Reason != "WRITE Flash Write Failed" {
		t.Fatalf("got %v, want WRITE error", err)
	}
}

func TestSendTimesOutWithoutReplies(t *testing.T) {
	shortTimeouts(t)
	l := newFakeLight()
	l.silent = true
	if err := newTestSession(t, l).Send(testImage(1000), nil); err == nil || !strings.Contains(err.Error(), "no reply") {
		t.Fatalf("got %v, want timeout", err)
	}
}

func TestSendReusesSession(t *testing.T) {
	l := newFakeLight()
	s := newTestSession(t, l)
	for i := 0; i < 2; i++ {
		img := testImage(5000 + i)
		if err := s.Send(img, nil); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if !bytes.Equal(l.got.Bytes(), img) {
			t.Fatalf("send %d: light received different bytes", i)
		}
	}
}

func TestValidateImage(t *testing.T) {
	if err := ValidateImage(testImage(100)); err != nil {
		t.Errorf("valid image rejected: %v", err)
	}
	bad := map[string][]byte{
		"empty":     nil,
		"magic":     {0x00, 1, 2},
		"too large": append([]byte{imageMagic}, make([]byte, MaxImageSize)...),
	}
	for name, b := range bad {
		if ValidateImage(b) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
