//go:build !linux

package modem

import "go.bug.st/serial"

func openSerialTransport(path string, baudRate int) (Transport, error) {
	return serial.Open(path, &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
}
