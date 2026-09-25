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
	sessionCancel chan struct{}
	stopOnce      sync.Once
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
		lastSentColor: 'G', // Default Green when connected
	}
}

func (c *Client) abortActiveSession() {
	c.mu.Lock()
	cancel := c.sessionCancel
	c.sessionCancel = nil
	c.mu.Unlock()

	if cancel != nil {
		select {
		case <-cancel:
		default:
			close(cancel)
		}
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
		addr := strings.ToUpper(device.Address.String())
		log.Printf("[BLE] Adapter link event for %s: connected=%v", addr, connected)

		if !connected {
			c.mu.Lock()
			activeMAC := c.targetMAC
			isConn := c.connected
			c.mu.Unlock()

			if isConn && (activeMAC == "" || strings.EqualFold(activeMAC, addr)) {
				log.Printf("[BLE] Active link lost for %s. Aborting session to reconnect immediately...", addr)
				c.abortActiveSession()
			}
		}
	})

	go c.lifecycleLoop()
	return nil
}

func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
		c.mu.Lock()
		if c.device != nil {
			_ = c.device.Disconnect()
		}
		c.mu.Unlock()
	})
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

	devCopy := device
	// Hand over connection to active session - maintain the connection without dropping!
	c.mu.Lock()
	c.targetMAC = macAddress
	c.targetName = friendlyName
	c.sharedSecret = secret
	c.device = &devCopy
	c.char = lightChar
	c.authChar = authChar
	c.connected = true
	c.authenticated = true
	c.mu.Unlock()

	log.Printf("[BLE] Paired successfully! Active session established with '%s' (%s).", friendlyName, macAddress)

	// Run active session maintainer in background
	go c.runSession(&devCopy, lightChar)

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

	c.abortActiveSession()

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

		// Windows WinRT settling delay after connection before discovering services.
		// Verify connection is alive before discovering services: device.Connected()
		// has been observed to still report false for a bit even when the Arduino's
		// own serial log confirms the connection genuinely succeeded, so a single
		// check right after one settle delay was giving up on good connections too
		// early. Poll a few times before concluding it really didn't come up.
		var conn bool
		var cErr error
		for attempt := 0; attempt < 4; attempt++ {
			time.Sleep(350 * time.Millisecond)
			conn, cErr = device.Connected()
			if cErr == nil && conn {
				break
			}
		}
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
				if strings.Contains(statusStr, "UNPAIRED") && c.confirmUnpaired(authChar) {
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
			time.Sleep(150 * time.Millisecond)
			respBuf := make([]byte, 32)
			n, readErr := authChar.Read(respBuf)
			if readErr != nil {
				log.Printf("[BLE] Auth response read failed: %v. Treating as auth failure.", readErr)
				_ = device.Disconnect()
				time.Sleep(3 * time.Second)
				continue
			}
			respStr := string(respBuf[:n])
			if (strings.Contains(respStr, "NOT_PAIRED") || strings.Contains(respStr, "UNPAIRED")) && c.confirmUnpaired(authChar) {
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
			if !strings.Contains(respStr, "AUTH_OK") {
				log.Printf("[BLE] Unexpected/empty auth response (%q). Treating as auth failure.", respStr)
				_ = device.Disconnect()
				time.Sleep(3 * time.Second)
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

// runSession drives an established connection until it drops. It performs its own
// WinRT thread/COM apartment setup (LockOSThread + RoInitialize) because it can be
// invoked either synchronously from lifecycleLoop's already-initialized goroutine, or
// spawned as a brand-new goroutine from PairDevice — the latter would otherwise make
// WinRT calls (device.Connected/Write/Disconnect) from an uninitialized OS thread,
// which is unsafe. LockOSThread/RoInitialize are both safe to nest on the same thread.
func (c *Client) runSession(device *bluetooth.Device, lightChar *bluetooth.DeviceCharacteristic) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.RoInitialize(1)
	defer ole.CoUninitialize()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from runSession panic: %v", r)
		}
	}()

	cancelChan := make(chan struct{})
	c.mu.Lock()
	c.sessionCancel = cancelChan
	c.mu.Unlock()

	defer c.abortActiveSession()

	log.Printf("[BLE] Service & Characteristics ready!")
	if c.onConnect != nil {
		c.onConnect()
	}

	// Settle delay to allow onConnect state callback to set lastSentColor
	time.Sleep(100 * time.Millisecond)

	// Send initial state immediately
	c.mu.Lock()
	initColor := c.lastSentColor
	c.mu.Unlock()
	_ = c.writeControl(lightChar, initColor)

	// Maintain connection & handle commands/heartbeats
	c.connectionLoop(device, lightChar, cancelChan)

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

		// 1. MAC match takes highest priority.
		//
		// This used to also compare the scanned advertisement's LocalName against the
		// expected paired name, and wipe the saved pairing if it looked like a factory
		// default ("SignalLight-XXXX"). That's a real, observed false-positive risk:
		// Windows' own BLE scan/device cache can serve a STALE advertised name for a MAC
		// address (commonly the very first name it ever saw the device advertise under,
		// e.g. before it was paired), even though the device is actually advertising
		// correctly over the air. That silently wiped a real, still-paired device's config
		// with no way to undo it. A genuine factory reset is still reliably detected right
		// after connecting via discoverCharacteristics + the STATUS/AUTH read below, which
		// forces an uncached GATT read of the actual device instead of trusting scan data.
		if targetMAC != "" {
			if addr == targetMAC {
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
		time.Sleep(time.Duration(attempt*200) * time.Millisecond)

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

// confirmUnpaired re-queries the device's pairing status directly ("STATUS?") as a
// second, independent check before an "unpaired" signal is trusted enough to wipe the
// local config. This link has been directly observed to corrupt GATT read payloads
// under degraded conditions (e.g. a garbled device name during a bad reconnect
// storm), and the unpair-detection checks below only do a loose substring match —
// a single corrupted read that happens to contain "UNPAIRED" as noise could
// otherwise destroy a real, still-valid pairing with no way to undo it. Requiring an
// independent re-read to agree makes that far less likely.
func (c *Client) confirmUnpaired(authChar *bluetooth.DeviceCharacteristic) bool {
	if authChar == nil {
		return false
	}
	if _, err := authChar.Write([]byte("STATUS?")); err != nil {
		return false
	}
	time.Sleep(150 * time.Millisecond)
	buf := make([]byte, 32)
	n, err := authChar.Read(buf)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(buf[:n])) == "STATUS:UNPAIRED"
}

// writePing sends 'P' using Write with response (acknowledged).
// We strictly do NOT fall back to WriteWithoutResponse here, because WriteWithoutResponse
// succeeds in Windows WinRT even when the peripheral is completely disconnected!
func (c *Client) writePing(char *bluetooth.DeviceCharacteristic) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from writePing panic: %v", r)
		}
	}()

	if char == nil {
		return errors.New("characteristic is nil")
	}

	_, err = char.Write([]byte{'P'})
	return err
}

// writeControl sends a control command byte ('R', 'Y', 'G', '0') using Write with
// response (acknowledged).
//
// This used to fall back to WriteWithoutResponse whenever device.Connected() didn't
// clearly report "disconnected" (including when Connected() itself errored). That
// fallback silently masked real failures: WriteWithoutResponse reports success in
// Windows WinRT even when the peripheral is completely disconnected (see writePing's
// comment above), and a stale/closed WinRT BLE object — a real, observed failure mode
// (HRESULT 0x80000013 "The object has been closed" / RO_E_CLOSED) — can make both the
// initial Write AND device.Connected() unreliable at the same time. The result was up
// to 60 seconds of heartbeats silently going nowhere before the Arduino's own watchdog
// gave up and reverted to Yellow, instead of a fast, honest reconnect. We now always
// surface a Write failure as a real error so the caller disconnects and reconnects
// immediately (which typically completes in a few seconds), the same way writePing does.
func (c *Client) writeControl(char *bluetooth.DeviceCharacteristic, cmd byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from writeControl panic: %v", r)
		}
	}()

	if char == nil {
		return errors.New("characteristic is nil")
	}

	_, err = char.Write([]byte{cmd})
	return err
}

func (c *Client) connectionLoop(device *bluetooth.Device, char *bluetooth.DeviceCharacteristic, cancelChan <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[BLE] Recovered from connectionLoop panic: %v", r)
		}
	}()

	heartbeat := time.NewTicker(3 * time.Second)
	defer heartbeat.Stop()

	heartbeatCounter := 0
	mismatchStreak := 0

	for {
		select {
		case <-c.stopChan:
			return
		case <-cancelChan:
			log.Println("[BLE] Connection loop aborted by link loss event.")
			return
		case cmd := <-c.sendChan:
			err := c.writeControl(char, cmd)
			if err != nil {
				log.Printf("[BLE] Command write error: %v. Connection lost.", err)
				return
			}
		case <-heartbeat.C:
			// 1. Check OS link status
			if isConn, err := device.Connected(); err == nil && !isConn {
				log.Println("[BLE] Device reports disconnected in OS stack. Connection lost.")
				return
			}

			// 2. Active state-sync heartbeat: Send current active color (e.g. 'G').
			// This satisfies the watchdog AND guarantees self-healing: if the Arduino
			// ever experienced a momentary timeout or glitch, the heartbeat immediately restores it!
			c.mu.Lock()
			activeColor := c.lastSentColor
			c.mu.Unlock()

			err := c.writeControl(char, activeColor)
			if err != nil {
				log.Printf("[BLE] Heartbeat error: %v. Connection lost.", err)
				return
			}

			// 3. Verify the write actually reached the device instead of blindly
			// trusting Write()'s success. This is a real, observed failure mode on
			// some BLE link/driver conditions: Write-with-response can report success
			// at the host/WinRT layer without the peripheral ever receiving the byte,
			// with no error surfaced anywhere — confirmed independent of USB/RF
			// proximity (reproduced even with the Arduino on separate power, 7ft
			// away). Reading the characteristic back (forced uncached, so it reflects
			// a real over-the-air round trip) after a short settle delay lets us
			// detect this and retry the write immediately.
			//
			// NOTE: an earlier version of this forced a full disconnect+reconnect
			// after a sustained mismatch. In practice that made recovery WORSE: it
			// exposed a separate, pre-existing bug where a fresh reconnect's own
			// device.Connected() check can unreliably report false even when the
			// Arduino confirms a real connection, turning a bounded ~60s Yellow
			// (the Arduino's own watchdog self-healing once a write finally gets
			// through) into an open-ended multi-minute outage stuck retrying a bad
			// reconnect path. So we now only ever retry the write on the SAME
			// still-alive connection here, and leave reconnect decisions entirely to
			// real OS-level signals (Connected()/Write()/Read() actually erroring).
			time.Sleep(150 * time.Millisecond)
			readBuf := make([]byte, 1)
			n, rErr := char.Read(readBuf)
			if rErr != nil {
				log.Printf("[BLE] Heartbeat verify-read error: %v. Connection lost.", rErr)
				return
			}
			if n < 1 || readBuf[0] != activeColor {
				mismatchStreak++
				log.Printf("[BLE] Heartbeat verify mismatch (device reports '%c', expected '%c'), streak=%d. Retrying write...", readBuf[0], activeColor, mismatchStreak)
				if retryErr := c.writeControl(char, activeColor); retryErr != nil {
					log.Printf("[BLE] Heartbeat retry write error: %v. Connection lost.", retryErr)
					return
				}
			} else {
				mismatchStreak = 0
			}

			heartbeatCounter++
			if heartbeatCounter%20 == 0 { // Log every 60s (20 * 3s)
				log.Printf("[BLE] Heartbeat sync healthy (active='%c')", activeColor)
			}
		}
	}
}
