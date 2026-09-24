package serial

import (
	"log"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

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
		lastSentColor: "YELLOW",
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
		_, _ = p.Write([]byte(initColor + "\n"))

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
			_, err := p.Write([]byte(cmd + "\n"))
			if err != nil {
				log.Printf("[Serial] Write error: %v", err)
				return
			}
		case <-heartbeat.C:
			_, err := p.Write([]byte("PING\n"))
			if err != nil {
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
