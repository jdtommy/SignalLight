"""
SignalLight - MicroPython for Arduino Nano ESP32 (ESP32-S3)

Hardware:
  - D2 (GPIO 5): RED LED
  - D3 (GPIO 6): YELLOW LED (Default ON)
  - D4 (GPIO 7): GREEN LED
"""

import time
import machine
import bluetooth
from micropython import const

# BLE Event constants
_IRQ_CENTRAL_CONNECT = const(1)
_IRQ_CENTRAL_DISCONNECT = const(2)
_IRQ_GATTS_WRITE = const(3)

# Nordic UART / Custom UUIDs
_SERVICE_UUID = bluetooth.UUID("19B10000-E8F2-537E-4F6C-D104768A1214")
_CHAR_UUID = bluetooth.UUID("19B10001-E8F2-537E-4F6C-D104768A1214")

# Setup Pins (Arduino Nano ESP32 D2, D3, D4 map to GPIO pins 5, 6, 7 in ESP32-S3 default mapping)
# Note: Adjust pin numbers if your MicroPython firmware uses board-labeled pin names.
try:
    pin_red = machine.Pin("D2", machine.Pin.OUT)
    pin_yellow = machine.Pin("D3", machine.Pin.OUT)
    pin_green = machine.Pin("D4", machine.Pin.OUT)
except ValueError:
    # Standard ESP32 GPIO fallback for Nano ESP32
    pin_red = machine.Pin(5, machine.Pin.OUT)
    pin_yellow = machine.Pin(6, machine.Pin.OUT)
    pin_green = machine.Pin(7, machine.Pin.OUT)

current_color = 'Y'
last_heartbeat = time.ticks_ms()
TIMEOUT_MS = 15000

def apply_color(c):
    global current_color
    current_color = c.upper()
    
    pin_red.value(0)
    pin_yellow.value(0)
    pin_green.value(0)
    
    if current_color == 'R':
        pin_red.value(1)
        print("[State] RED (In Meeting)")
    elif current_color == 'G':
        pin_green.value(1)
        print("[State] GREEN (Available)")
    else:
        pin_yellow.value(1)
        current_color = 'Y'
        print("[State] YELLOW (Away / Default)")

class BLELight:
    def __init__(self, ble, name="SignalLight"):
        self._ble = ble
        self._ble.active(True)
        self._ble.irq(self._irq)
        
        SERVICE = (
            _SERVICE_UUID,
            ((_CHAR_UUID, bluetooth.FLAG_READ | bluetooth.FLAG_WRITE | bluetooth.FLAG_NOTIFY),),
        )
        ((self._handle,),) = self._ble.gatts_register_services((SERVICE,))
        self._connections = set()
        self._advertise(name)
        
    def _irq(self, event, data):
        global last_heartbeat
        if event == _IRQ_CENTRAL_CONNECT:
            conn_handle, _, _ = data
            self._connections.add(conn_handle)
            print("[BLE] Connected")
            last_heartbeat = time.ticks_ms()
        elif event == _IRQ_CENTRAL_DISCONNECT:
            conn_handle, _, _ = data
            self._connections.remove(conn_handle)
            print("[BLE] Disconnected")
            self._advertise()
        elif event == _IRQ_GATTS_WRITE:
            conn_handle, value_handle = data
            if value_handle == self._handle:
                value = self._ble.gatts_read(self._handle)
                if value:
                    cmd = chr(value[0]).upper()
                    last_heartbeat = time.ticks_ms()
                    if cmd in ('R', 'G', 'Y'):
                        apply_color(cmd)

    def _advertise(self, name="SignalLight"):
        name_bytes = bytes(name, "UTF-8")
        payload = bytearray(
            b"\x02\x01\x06" +
            bytes([len(name_bytes) + 1, 0x09]) +
            name_bytes
        )
        self._ble.gap_advertise(100000, payload)
        print(f"[BLE] Advertising as {name}")

def main():
    apply_color('Y')
    ble = bluetooth.BLE()
    light = BLELight(ble)
    
    global last_heartbeat
    print("SignalLight MicroPython started.")
    
    while True:
        # Failsafe watchdog
        if time.ticks_diff(time.ticks_ms(), last_heartbeat) > TIMEOUT_MS:
            if current_color != 'Y':
                print("[Watchdog] Timeout. Defaulting to YELLOW.")
                apply_color('Y')
            last_heartbeat = time.ticks_ms()
        time.sleep_ms(200)

if __name__ == "__main__":
    main()
