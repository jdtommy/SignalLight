package ble

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-ole/go-ole"
	"tinygo.org/x/bluetooth"
)

var (
	ServiceUUID, _            = bluetooth.ParseUUID("19B10000-E8F2-537E-4F6C-D104768A1214")
	CharacteristicUUID, _     = bluetooth.ParseUUID("19B10001-E8F2-537E-4F6C-D104768A1214")
	AuthCharacteristicUUID, _ = bluetooth.ParseUUID("19B10002-E8F2-537E-4F6C-D104768A1214")
)

type DiscoveredDevice struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	RSSI     int16  `json:"rssi"`
	IsPaired bool   `json:"is_paired"`
}

type Client struct {
	adapter       *bluetooth.Adapter
	device        *bluetooth.Device
	char          *bluetooth.DeviceCharacteristic
	authChar      *bluetooth.DeviceCharacteristic
	mu            sync.Mutex
	scanMu        sync.Mutex
	connected     bool
	authenticated bool
	isPairing     bool
	onConnect     func()
	onDisconnect  func()
	onUnpaired    func()
	stopChan      chan struct{}
	sendChan      chan byte
	targetMAC     string
	targetName    string
	sharedSecret  string
	lastSentColor byte
}

func NewClient(targetMAC, targetName, sharedSecret string, onConnect func(), onDisconnect func()) *Client {
	return &Client{
		adapter:       bluetooth.DefaultAdapter,
		onConnect:     onConnect,
		onDisconnect:  onDisconnect,
		stopChan:      make(chan struct{}),
		sendChan:      make(chan byte, 32),
		targetMAC:     strings.ToUpper(strings.TrimSpace(targetMAC)),
		targetName:    strings.TrimSpace(targetName),
		sharedSecret:  strings.TrimSpace(sharedSecret),
		lastSentColor: 'Y', // Default Yellow
	}
}

// SetOnUnpaired sets the callback for when a device reports it has been factory reset / unpaired.
func (c *Client) SetOnUnpaired(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUnpaired = fn
}

// SetTarget updates the target device parameters dynamically.
func (c *Client) SetTarget(mac, name, secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targetMAC = strings.ToUpper(strings.TrimSpace(mac))
	c.targetName = strings.TrimSpace(name)
	c.sharedSecret = strings.TrimSpace(secret)
	log.Printf("[BLE] Target updated: MAC='%s', Name='%s'", c.targetMAC, c.targetName)
}

// Start initiates the BLE client loop.
func (c *Client) Start() error {
	if err := c.adapter.Enable(); err != nil {
		return fmt.Errorf("failed to enable bluetooth adapter: %w", err)
	}

	c.adapter.SetConnectHandler(func(device bluetooth.Device, connected bool) {
		log.Printf("[BLE] Adapter link event for %s: connected=%v", device.Address.String(), connected)
	})

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
	return c.connected && c.authenticated
}

// ScanNearbyDevices scans for the specified duration and returns all discovered SignalLight devices.
func (c *Client) ScanNearbyDevices(timeout time.Duration) (devices []DiscoveredDevice, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.RoInitialize(1)
	defer ole.CoUninitialize()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from scan exception: %v", r)
			err = fmt.Errorf("bluetooth scan error: %v", r)
		}
	}()

	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	if timeout <= 0 {
		timeout = 6 * time.Second
	}

	deviceMap := make(map[string]DiscoveredDevice)
	var mapMu sync.Mutex

	stopTimer := time.AfterFunc(timeout, func() {
		_ = c.adapter.StopScan()
	})
	defer stopTimer.Stop()

	log.Printf("[BLE] Starting discovery scan for %v...", timeout)
	scanErr := c.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		addr := strings.ToUpper(result.Address.String())
		name := strings.TrimSpace(result.LocalName())
		hasService := result.HasServiceUUID(ServiceUUID)
		isSignalLightName := strings.HasPrefix(strings.ToLower(name), "signallight")

		mapMu.Lock()
		defer mapMu.Unlock()

		existing, alreadyFound := deviceMap[addr]

		// Filter out non-SignalLight devices:
		// Accept if it advertises our ServiceUUID, has a SignalLight name, or we already
		// identified this MAC address in a previous packet during this scan session.
		if !hasService && !isSignalLightName && !alreadyFound {
			return
		}

		// Determine best name across advertising packets (ADV_IND and SCAN_RSP)
		finalName := ""
		if name != "" {
			finalName = name
		} else if alreadyFound && existing.Name != "" && existing.Name != "SignalLight (Unpaired)" {
			finalName = existing.Name
		} else {
			finalName = "SignalLight (Unpaired)"
		}

		isPaired := false
		if finalName != "" && !strings.HasPrefix(strings.ToLower(finalName), "signallight-") && !strings.EqualFold(finalName, "signallight") && finalName != "SignalLight (Unpaired)" {
			isPaired = true
		}

		rssi := result.RSSI
		if rssi == 0 && alreadyFound {
			rssi = existing.RSSI
		}

		deviceMap[addr] = DiscoveredDevice{
			Name:     finalName,
			Address:  addr,
			RSSI:     rssi,
			IsPaired: isPaired,
		}
	})

	log.Printf("[BLE] Scan complete. Found %d SignalLight device(s).", len(deviceMap))
	if scanErr != nil {
		return nil, scanErr
	}

	mapMu.Lock()
	defer mapMu.Unlock()

	devices = make([]DiscoveredDevice, 0, len(deviceMap))
	for _, dev := range deviceMap {
		devices = append(devices, dev)
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].RSSI > devices[j].RSSI
	})

	return devices, nil
}

// PairDevice connects to a specified device, sends the PAIR command, and verifies pairing.
func (c *Client) PairDevice(macAddress string, friendlyName string, secret string) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.RoInitialize(1)
	defer ole.CoUninitialize()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from pairing exception: %v", r)
			err = fmt.Errorf("bluetooth pairing exception: %v", r)
		}
	}()

	macAddress = strings.ToUpper(strings.TrimSpace(macAddress))
	friendlyName = strings.TrimSpace(friendlyName)
	secret = strings.TrimSpace(secret)

	if macAddress == "" || friendlyName == "" || secret == "" {
		return errors.New("mac address, name, and secret must not be empty")
	}

	// Tell background loop to pause while pairing
	c.mu.Lock()
	c.isPairing = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.isPairing = false
		c.mu.Unlock()
	}()

	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	log.Printf("[BLE] Starting pairing with %s ('%s')...", macAddress, friendlyName)

	var targetResult *bluetooth.ScanResult
	stopTimer := time.AfterFunc(6*time.Second, func() {
		_ = c.adapter.StopScan()
	})
	defer stopTimer.Stop()

	scanErr := c.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		if strings.ToUpper(result.Address.String()) == macAddress {
			targetResult = &result
			_ = adapter.StopScan()
		}
	})

	if scanErr != nil {
		return fmt.Errorf("scan error during pairing: %w", scanErr)
	}

	if targetResult == nil {
		return fmt.Errorf("could not find device %s within 6s", macAddress)
	}

	log.Printf("[BLE] Connecting to %s for pairing...", macAddress)
	device, err := c.adapter.Connect(targetResult.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("failed to connect for pairing: %w", err)
	}

	// Windows WinRT settling delay
	time.Sleep(1 * time.Second)

	conn, cErr := device.Connected()
	if cErr != nil || !conn {
		_ = device.Disconnect()
		return fmt.Errorf("device disconnected before pairing could complete (conn=%v, err=%v)", conn, cErr)
	}

	// Discover service and characteristics
	lightChar, authChar, err := c.discoverCharacteristics(&device)
	if err != nil || authChar == nil {
		_ = device.Disconnect()
		return fmt.Errorf("failed to discover characteristics: %w", err)
	}

	pairCmd := fmt.Sprintf("PAIR:%s:%s", friendlyName, secret)
	log.Printf("[BLE] Sending pairing packet to %s...", macAddress)
	if _, err := authChar.Write([]byte(pairCmd)); err != nil {
		_ = device.Disconnect()
		return fmt.Errorf("failed to write pairing packet: %w", err)
	}

	time.Sleep(400 * time.Millisecond)

	// Hand over connection to active session - maintain the connection without dropping!
	c.mu.Lock()
	c.targetMAC = macAddress
	c.targetName = friendlyName
	c.sharedSecret = secret
	c.device = &device
	c.char = lightChar
	c.authChar = authChar
	c.connected = true
	c.authenticated = true
	c.mu.Unlock()

	log.Printf("[BLE] Paired successfully! Active session established with '%s' (%s).", friendlyName, macAddress)

	// Run active session maintainer in background
	go c.runSession(&device, lightChar)

	return nil
}

// UnpairCurrent sends an UNPAIR command to the currently connected device and clears credentials.
func (c *Client) UnpairCurrent() (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.RoInitialize(1)
	defer ole.CoUninitialize()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from unpair exception: %v", r)
			err = fmt.Errorf("unpair exception: %v", r)
		}
	}()

	c.mu.Lock()
	authChar := c.authChar
	device := c.device
	c.mu.Unlock()

	if authChar != nil {
		log.Println("[BLE] Sending UNPAIR command to connected device...")
		_, _ = authChar.Write([]byte("UNPAIR"))
		time.Sleep(400 * time.Millisecond)
	}

	c.SetTarget("", "", "")

	if device != nil {
		_ = device.Disconnect()
	}

	c.mu.Lock()
	c.connected = false
	c.authenticated = false
	c.device = nil
	c.char = nil
	c.authChar = nil
	c.mu.Unlock()

	return nil
}

func (c *Client) lifecycleLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.RoInitialize(1)
	defer ole.CoUninitialize()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Caught unexpected loop exception: %v. Restarting in 4s...", r)
			time.Sleep(4 * time.Second)
			go c.lifecycleLoop()
		}
	}()

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		c.mu.Lock()
		targetMAC := c.targetMAC
		targetName := c.targetName
		secret := c.sharedSecret
		isPairing := c.isPairing
		isConnected := c.connected
		c.mu.Unlock()

		if isPairing || isConnected {
			time.Sleep(1 * time.Second)
			continue
		}

		if targetMAC == "" && targetName == "" {
			// Unconfigured / waiting for user to pair via Web UI
			time.Sleep(2 * time.Second)
			continue
		}

		log.Printf("[BLE] Searching for target MAC='%s' Name='%s'...", targetMAC, targetName)
		foundDevice, err := c.scanForTarget(targetMAC, targetName)
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

		// Windows WinRT requires a settling delay after connection before discovering services
		time.Sleep(1 * time.Second)

		// Verify connection is alive before discovering services
		conn, cErr := device.Connected()
		if cErr != nil || !conn {
			log.Printf("[BLE] Device link not established (conn=%v, err=%v). Retrying in 2s...", conn, cErr)
			_ = device.Disconnect()
			time.Sleep(2 * time.Second)
			continue
		}

		log.Printf("[BLE] Connected! Discovering services...")
		lightChar, authChar, err := c.discoverCharacteristics(&device)
		if err != nil {
			log.Printf("[BLE] Failed to discover characteristics: %v. Disconnecting...", err)
			_ = device.Disconnect()
			time.Sleep(3 * time.Second)
			continue
		}

		// Check if device is in factory unpaired mode
		if authChar != nil {
			statusBuf := make([]byte, 32)
			if n, rErr := authChar.Read(statusBuf); rErr == nil {
				statusStr := string(statusBuf[:n])
				if strings.Contains(statusStr, "UNPAIRED") {
					log.Println("[BLE] Connected device reported STATUS:UNPAIRED (hardware reset detected)! Clearing saved pairing.")
					_ = device.Disconnect()
					c.SetTarget("", "", "")
					c.mu.Lock()
					unpairedCb := c.onUnpaired
					c.mu.Unlock()
					if unpairedCb != nil {
						unpairedCb()
					}
					time.Sleep(2 * time.Second)
					continue
				}
			}
		}

		// Perform Authentication Handshake if secret is present
		authenticated := false
		if secret != "" && authChar != nil {
			authCmd := "AUTH:" + secret
			log.Println("[BLE] Authenticating with shared secret...")
			_, err := authChar.Write([]byte(authCmd))
			if err != nil {
				log.Printf("[BLE] Auth write failed: %v. Disconnecting.", err)
				_ = device.Disconnect()
				time.Sleep(3 * time.Second)
				continue
			}

			// Read response buffer
			time.Sleep(300 * time.Millisecond)
			respBuf := make([]byte, 32)
			n, _ := authChar.Read(respBuf)
			respStr := string(respBuf[:n])
			if strings.Contains(respStr, "NOT_PAIRED") || strings.Contains(respStr, "UNPAIRED") {
				log.Println("[BLE] Device is in factory UNPAIRED mode (hardware reset detected)! Clearing saved pairing.")
				_ = device.Disconnect()
				c.SetTarget("", "", "")
				c.mu.Lock()
				unpairedCb := c.onUnpaired
				c.mu.Unlock()
				if unpairedCb != nil {
					unpairedCb()
				}
				time.Sleep(2 * time.Second)
				continue
			}
			if strings.Contains(respStr, "AUTH_FAIL") {
				log.Println("[BLE] Security error: Arduino rejected secret (AUTH_FAIL)!")
				_ = device.Disconnect()
				time.Sleep(4 * time.Second)
				continue
			}
			authenticated = true
			log.Println("[BLE] Authentication successful!")
		} else {
			authenticated = true
		}

		c.mu.Lock()
		c.device = &device
		c.char = lightChar
		c.authChar = authChar
		c.connected = true
		c.authenticated = authenticated
		c.mu.Unlock()

		c.runSession(&device, lightChar)

		time.Sleep(2 * time.Second)
	}
}

func (c *Client) runSession(device *bluetooth.Device, lightChar *bluetooth.DeviceCharacteristic) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from runSession panic: %v", r)
		}
	}()

	log.Printf("[BLE] Service & Characteristics ready!")
	if c.onConnect != nil {
		c.onConnect()
	}

	// Send initial state immediately
	c.mu.Lock()
	initColor := c.lastSentColor
	c.mu.Unlock()
	_ = c.writeChar(lightChar, []byte{initColor})

	// Maintain connection & handle commands/heartbeats
	c.connectionLoop(device, lightChar)

	if device != nil {
		_ = device.Disconnect()
	}

	// Disconnected
	c.mu.Lock()
	c.connected = false
	c.authenticated = false
	c.device = nil
	c.char = nil
	c.authChar = nil
	c.mu.Unlock()

	log.Printf("[BLE] Disconnected from device.")
	if c.onDisconnect != nil {
		c.onDisconnect()
	}
}

func (c *Client) scanForTarget(targetMAC, targetName string) (*bluetooth.ScanResult, error) {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	var foundResult *bluetooth.ScanResult

	stopTimer := time.AfterFunc(8*time.Second, func() {
		_ = c.adapter.StopScan()
	})
	defer stopTimer.Stop()

	scanErr := c.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		addr := strings.ToUpper(result.Address.String())
		name := strings.TrimSpace(result.LocalName())

		// 1. MAC match takes highest priority
		if targetMAC != "" {
			if addr == targetMAC {
				// If we expected a paired friendly name (e.g. "Jarads"), but the device is now
				// advertising with the factory unpaired default name ("SignalLight-XXXX"):
				// A hardware factory reset was performed on the device!
				if targetName != "" && !strings.EqualFold(name, targetName) && strings.HasPrefix(strings.ToLower(name), "signallight-") {
					log.Printf("[BLE] Target device %s has reverted to factory unpaired name ('%s')! Clearing pairing.", addr, name)
					c.SetTarget("", "", "")
					c.mu.Lock()
					unpairedCb := c.onUnpaired
					c.mu.Unlock()
					if unpairedCb != nil {
						unpairedCb()
					}
					_ = adapter.StopScan()
					return
				}

				foundResult = &result
				_ = adapter.StopScan()
				return
			}
		}

		// 2. Name match fallback
		if targetName != "" && strings.EqualFold(name, targetName) {
			foundResult = &result
			_ = adapter.StopScan()
			return
		}

		// 3. Fallback: if no specific MAC/Name, match Service UUID or "SignalLight"
		if targetMAC == "" && targetName == "" {
			if strings.HasPrefix(strings.ToLower(name), "signallight") || result.HasServiceUUID(ServiceUUID) {
				foundResult = &result
				_ = adapter.StopScan()
			}
		}
	})

	if scanErr != nil {
		return nil, scanErr
	}

	return foundResult, nil
}

func (c *Client) discoverCharacteristics(device *bluetooth.Device) (lightChar *bluetooth.DeviceCharacteristic, authChar *bluetooth.DeviceCharacteristic, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from WinRT service discovery panic: %v", r)
			err = fmt.Errorf("service discovery panic: %v", r)
		}
	}()

	if device == nil {
		return nil, nil, errors.New("device is nil")
	}

	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(time.Duration(attempt*400) * time.Millisecond)

		services, sErr := device.DiscoverServices([]bluetooth.UUID{ServiceUUID})
		if sErr != nil || len(services) == 0 {
			err = fmt.Errorf("service %s not found (attempt %d/3): %v", ServiceUUID.String(), attempt, sErr)
			continue
		}

		chars, cErr := services[0].DiscoverCharacteristics([]bluetooth.UUID{CharacteristicUUID, AuthCharacteristicUUID})
		if cErr != nil || len(chars) == 0 {
			err = fmt.Errorf("characteristics not found (attempt %d/3): %v", attempt, cErr)
			continue
		}

		for i := range chars {
			if chars[i].UUID() == CharacteristicUUID {
				lightChar = &chars[i]
			} else if chars[i].UUID() == AuthCharacteristicUUID {
				authChar = &chars[i]
			}
		}

		if lightChar != nil {
			return lightChar, authChar, nil
		}
	}
	return nil, nil, err
}

func (c *Client) writeChar(char *bluetooth.DeviceCharacteristic, data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from writeChar panic: %v", r)
		}
	}()

	if char == nil {
		return errors.New("characteristic is nil")
	}
	_, err = char.Write(data)
	if err != nil {
		_, err = char.WriteWithoutResponse(data)
	}
	return err
}

func (c *Client) connectionLoop(device *bluetooth.Device, char *bluetooth.DeviceCharacteristic) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from connectionLoop panic: %v", r)
		}
	}()

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
			err := c.writeChar(char, []byte{'P'})
			if err != nil {
				log.Printf("[BLE] Heartbeat error: %v. Connection lost.", err)
				return
			}
		}
	}
}
