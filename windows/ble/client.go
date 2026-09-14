package ble

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

var (
	ServiceUUID, _        = bluetooth.ParseUUID("19B10000-E8F2-537E-4F6C-D104768A1214")
	CharacteristicUUID, _ = bluetooth.ParseUUID("19B10001-E8F2-537E-4F6C-D104768A1214")
)

type Client struct {
	adapter       *bluetooth.Adapter
	device        *bluetooth.Device
	char          *bluetooth.DeviceCharacteristic
	mu            sync.Mutex
	connected     bool
	onConnect     func()
	onDisconnect  func()
	stopChan      chan struct{}
	sendChan      chan byte
	targetName    string
	lastSentColor byte
}

func NewClient(targetName string, onConnect func(), onDisconnect func()) *Client {
	if targetName == "" {
		targetName = "SignalLight"
	}
	return &Client{
		adapter:       bluetooth.DefaultAdapter,
		onConnect:     onConnect,
		onDisconnect:  onDisconnect,
		stopChan:      make(chan struct{}),
		sendChan:      make(chan byte, 32),
		targetName:    targetName,
		lastSentColor: 'Y', // Default Yellow
	}
}

// Start initiates the BLE client loop (scan -> connect -> maintain -> reconnect).
func (c *Client) Start() error {
	if err := c.adapter.Enable(); err != nil {
		return fmt.Errorf("failed to enable bluetooth adapter: %w", err)
	}

	go c.lifecycleLoop()
	return nil
}

func (c *Client) Stop() {
	close(c.stopChan)
	c.mu.Lock()
	if c.device != nil {
		_ = c.device.Disconnect()
	}
	c.mu.Unlock()
}

// SendColor sends color command ('R', 'Y', 'G', '0') to the Arduino.
func (c *Client) SendColor(colorStr string) {
	var cmd byte
	switch strings.ToUpper(colorStr) {
	case "RED":
		cmd = 'R'
	case "YELLOW":
		cmd = 'Y'
	case "GREEN":
		cmd = 'G'
	case "OFF":
		cmd = '0'
	default:
		return
	}

	c.mu.Lock()
	c.lastSentColor = cmd
	c.mu.Unlock()

	select {
	case c.sendChan <- cmd:
	default:
		// Drain and put latest
		select {
		case <-c.sendChan:
		default:
		}
		c.sendChan <- cmd
	}
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Client) lifecycleLoop() {
	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		log.Printf("[BLE] Scanning for device '%s'...", c.targetName)
		foundDevice, err := c.scanForDevice()
		if err != nil {
			log.Printf("[BLE] Scan error: %v. Retrying in 3s...", err)
			time.Sleep(3 * time.Second)
			continue
		}

		if foundDevice == nil {
			continue
		}

		log.Printf("[BLE] Connecting to %s (%s)...", foundDevice.LocalName(), foundDevice.Address.String())
		device, err := c.adapter.Connect(foundDevice.Address, bluetooth.ConnectionParams{})
		if err != nil {
			log.Printf("[BLE] Connection failed: %v. Retrying in 3s...", err)
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[BLE] Connected! Discovering services...")
		char, err := c.discoverCharacteristic(&device)
		if err != nil {
			log.Printf("[BLE] Failed to discover characteristic: %v", err)
			_ = device.Disconnect()
			time.Sleep(2 * time.Second)
			continue
		}

		c.mu.Lock()
		c.device = &device
		c.char = char
		c.connected = true
		c.mu.Unlock()

		log.Printf("[BLE] Service & Characteristic ready!")
		if c.onConnect != nil {
			c.onConnect()
		}

		// Send initial state immediately
		c.mu.Lock()
		initColor := c.lastSentColor
		c.mu.Unlock()
		_ = c.writeChar(char, []byte{initColor})

		// Maintain connection & handle commands/heartbeats
		c.connectionLoop(&device, char)

		// Disconnected
		c.mu.Lock()
		c.connected = false
		c.device = nil
		c.char = nil
		c.mu.Unlock()

		log.Printf("[BLE] Disconnected from device.")
		if c.onDisconnect != nil {
			c.onDisconnect()
		}

		time.Sleep(2 * time.Second)
	}
}

func (c *Client) scanForDevice() (*bluetooth.ScanResult, error) {
	var foundResult *bluetooth.ScanResult
	foundChan := make(chan bluetooth.ScanResult, 1)

	scanErr := c.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		name := result.LocalName()
		if strings.EqualFold(name, c.targetName) || result.HasServiceUUID(ServiceUUID) {
			_ = adapter.StopScan()
			select {
			case foundChan <- result:
			default:
			}
		}
	})

	if scanErr != nil {
		return nil, scanErr
	}

	select {
	case res := <-foundChan:
		foundResult = &res
		return foundResult, nil
	case <-time.After(10 * time.Second):
		_ = c.adapter.StopScan()
		return nil, nil
	case <-c.stopChan:
		_ = c.adapter.StopScan()
		return nil, nil
	}
}

func (c *Client) discoverCharacteristic(device *bluetooth.Device) (*bluetooth.DeviceCharacteristic, error) {
	services, err := device.DiscoverServices([]bluetooth.UUID{ServiceUUID})
	if err != nil || len(services) == 0 {
		return nil, fmt.Errorf("service %s not found: %w", ServiceUUID.String(), err)
	}

	chars, err := services[0].DiscoverCharacteristics([]bluetooth.UUID{CharacteristicUUID})
	if err != nil || len(chars) == 0 {
		return nil, fmt.Errorf("characteristic %s not found: %w", CharacteristicUUID.String(), err)
	}

	return &chars[0], nil
}

func (c *Client) writeChar(char *bluetooth.DeviceCharacteristic, data []byte) error {
	_, err := char.Write(data)
	if err != nil {
		_, err = char.WriteWithoutResponse(data)
	}
	return err
}

func (c *Client) connectionLoop(device *bluetooth.Device, char *bluetooth.DeviceCharacteristic) {
	heartbeat := time.NewTicker(4 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case cmd := <-c.sendChan:
			err := c.writeChar(char, []byte{cmd})
			if err != nil {
				log.Printf("[BLE] Write error: %v. Connection lost.", err)
				return
			}
		case <-heartbeat.C:
			// Send heartbeat 'P' to keep Arduino watchdog happy
			err := c.writeChar(char, []byte{'P'})
			if err != nil {
				log.Printf("[BLE] Heartbeat error: %v. Connection lost.", err)
				return
			}
		}
	}
}
