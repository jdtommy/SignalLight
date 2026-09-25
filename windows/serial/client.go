package serial

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

const writeTimeout = 2 * time.Second

// writeWithTimeout guards against a stalled USB link blocking the connection loop
// forever: go.bug.st/serial's Write has no built-in deadline, so a wedged driver or
// unplugged-but-not-yet-detected device would otherwise hang here indefinitely,
// silently freezing heartbeats and color commands. On timeout we force-close the
// port to unblock the abandoned write and surface the stall as a disconnect.
func writeWithTimeout(p serial.Port, data []byte, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, err := p.Write(data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = p.Close()
		return fmt.Errorf("write timed out after %v", timeout)
	}
}

type Client struct {
	portName      string
	port          serial.Port
	mu            sync.Mutex
	connected     bool
	onConnect     func()
	onDisconnect  func()
	stopChan      chan struct{}
	sendChan      chan string
	lastSentColor string
}

func NewClient(portName string, onConnect func(), onDisconnect func()) *Client {
	return &Client{
		portName:      portName,
		onConnect:     onConnect,
		onDisconnect:  onDisconnect,
		stopChan:      make(chan struct{}),
		sendChan:      make(chan string, 32),
		lastSentColor: "GREEN",
	}
}

func (c *Client) Start() {
	go c.lifecycleLoop()
}

func (c *Client) Stop() {
	close(c.stopChan)
	c.mu.Lock()
	if c.port != nil {
		_ = c.port.Close()
	}
	c.mu.Unlock()
}

func (c *Client) SendColor(colorStr string) {
	c.mu.Lock()
	c.lastSentColor = colorStr
	c.mu.Unlock()

	select {
	case c.sendChan <- colorStr:
	default:
		select {
		case <-c.sendChan:
		default:
		}
		c.sendChan <- colorStr
	}
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Client) lifecycleLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Serial] Caught serial driver exception: %v. Restarting in 3s...", r)
			time.Sleep(3 * time.Second)
			go c.lifecycleLoop()
		}
	}()

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		targetPort := c.portName
		if targetPort == "" || strings.EqualFold(targetPort, "AUTO") {
			targetPort = findArduinoPort()
		}

		if targetPort == "" {
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[Serial] Attempting connection to %s at 115200 baud...", targetPort)
		mode := &serial.Mode{
			BaudRate: 115200,
		}
		p, err := serial.Open(targetPort, mode)
		if err != nil {
			log.Printf("[Serial] Could not open %s: %v. Retrying in 3s...", targetPort, err)
			time.Sleep(3 * time.Second)
			continue
		}

		c.mu.Lock()
		c.port = p
		c.connected = true
		c.mu.Unlock()

		log.Printf("[Serial] Connected to %s!", targetPort)
		if c.onConnect != nil {
			c.onConnect()
		}

		// Send initial state
		c.mu.Lock()
		initColor := c.lastSentColor
		c.mu.Unlock()
		_ = writeWithTimeout(p, []byte(initColor+"\n"), writeTimeout)

		// Maintain loop
		c.connectionLoop(p)

		// Disconnected
		c.mu.Lock()
		c.connected = false
		c.port = nil
		c.mu.Unlock()

		log.Printf("[Serial] Disconnected from %s.", targetPort)
		if c.onDisconnect != nil {
			c.onDisconnect()
		}

		time.Sleep(2 * time.Second)
	}
}

func (c *Client) connectionLoop(p serial.Port) {
	heartbeat := time.NewTicker(4 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case cmd := <-c.sendChan:
			if err := writeWithTimeout(p, []byte(cmd+"\n"), writeTimeout); err != nil {
				log.Printf("[Serial] Write error: %v", err)
				return
			}
		case <-heartbeat.C:
			if err := writeWithTimeout(p, []byte("PING\n"), writeTimeout); err != nil {
				log.Printf("[Serial] Heartbeat error: %v", err)
				return
			}
		}
	}
}

func findArduinoPort() string {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return ""
	}

	for _, p := range ports {
		if p.IsUSB {
			// Look for Arduino or ESP32 USB VID/PID or Product string
			desc := strings.ToLower(p.Product + " " + p.Manufacturer)
			if strings.Contains(desc, "arduino") || strings.Contains(desc, "esp32") || strings.Contains(desc, "ch340") || strings.Contains(desc, "cp210") {
				return p.Name
			}
		}
	}

	// Fallback to the first USB COM port if only 1 exists
	if len(ports) == 1 && ports[0].IsUSB {
		return ports[0].Name
	}

	return ""
}
