package transports

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/angelodlfrtr/serial"
	"github.com/jaster-prj/go-can"
)

// USBCanAnalyzer define a USBCanAnalyzer connection to canbus via serial connection on USB
type USBCanAnalyzer struct {
	// Port is the serial port eg : COM0 on windows, /dev/ttytest on posix, etc
	Port string

	// BaudRate is the serial connection baud rate
	BaudRate int

	// client is the serial.Port instance
	client *serial.Port

	// dataBuf contain data received by serial connection
	dataBuf []byte

	// mutex to access dataBuf
	mutex sync.Mutex

	// readErr is set if listen encounter an error during the read, readErr is set
	readErr error

	// running is read goroutine running
	running bool

	// readChan is a chan for reading frames
	readChan chan *can.Frame
}

func (t *USBCanAnalyzer) run() {
	t.running = true
	t.readChan = make(chan *can.Frame)

	go func() {
		for {
			// Stop goroutine if t.running == false
			t.mutex.Lock()
			running := t.running
			t.mutex.Unlock()

			if !running {
				break
			}

			// 64 byte read buffer
			data := make([]byte, 64)

			// Read data (in a blocking way)
			n, err := t.client.Read(data)

			if errors.Is(err, io.EOF) {
				continue
			}

			t.readErr = err
			if err != nil {
				continue
			}

			// Append to global data buf
			t.mutex.Lock()
			t.dataBuf = append(t.dataBuf, data[:n]...)
			t.mutex.Unlock()

			// Publish frames on channel
			for {
				if ok := t.publishFrames(); !ok {
					break
				}
			}
		}
	}()
}

func (t *USBCanAnalyzer) publishFrames() bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Find adapter start of frame
	for {
		// Stop if buffer is empty
		if len(t.dataBuf) == 0 {
			break
		}

		// Stop if found SOF
		if t.dataBuf[0] == 0xAA {
			break
		}

		// Remove one element from dataBuf and loop again
		t.dataBuf = t.dataBuf[1:]
	}

	// Check if data can contain an entire frame (min frame size is 5 in case of 0 data)
	// Else read serial
	// (SOF + 2 + DLC + EOF) = 5
	if len(t.dataBuf) < 5 {
		return false
	}

	// Create new frame
	frm := &can.Frame{}

	// DLC
	frm.DLC = t.dataBuf[1] - 0xC0

	// Check buffer len can contain a frame
	// else read serial
	if len(t.dataBuf) < 5+int(frm.DLC) {
		return false
	}

	// Validate frame
	// Check frame end with 0x55
	// The USB cananalyzer have bug and soemtimes returns wrong data fields
	if t.dataBuf[4+int(frm.DLC)] != 0x55 {
		// Ignore frame by juste removing the frame SOF
		// The frame will be ignored at next iteration
		t.dataBuf = t.dataBuf[1:]

		// @TODO: Maybe return an error here ?
		return false
	}

	// Arbitration ID
	frm.ArbitrationID = uint32(binary.LittleEndian.Uint16(t.dataBuf[2:]))

	// Data
	for i := 0; i < int(frm.DLC); i++ {
		frm.Data[i] = t.dataBuf[i+4]
	}

	// Resize t.dataBuf
	lastMsgLen := 1 + 1 + 2 + frm.DLC + 1 // 0xAA (SOF) + DLC + arbId + data + 0x55 (EOF)
	t.dataBuf = t.dataBuf[lastMsgLen:]

	// Publish frame
	t.readChan <- frm

	return true
}

// Open a serial connection
// Show https://github.com/kobolt/usb-can/blob/master/canusb.c for protocol definition
func (t *USBCanAnalyzer) Open() error {
	serialConfig := &serial.Config{
		// Name of the serial port
		Name: t.Port,

		// Baud rate should normally be 2 000 000
		Baud: t.BaudRate,

		// ReadTimeout for the connection. If zero, the Read() operation is blocking
		// ReadTimeout: 100 * time.Millisecond,
		ReadTimeout: 0,

		// Size is 8 databytes for USBCanAnalyzer
		Size: 8,

		// StopBits is 1 for usbCanAnalyzer
		StopBits: 1,

		// Parity none for usbCanAnalyzer
		Parity: serial.ParityNone,
	}

	port, err := serial.OpenPort(serialConfig)
	if err != nil {
		return err
	}

	t.client = port

	// Send initialization sequence (configure adapter)
	seq := []byte{
		0xAA,
		0x55,
		0x12,
		0x07,
		0x01,
		0x00,
		0x00,
		0x00,
		0x00,
		0x00,
		0x00,
		0x00,
		0x00,
		0x00,
		0x01,
		0x00,
		0x00,
		0x00,
		0x00,
		0x1B,
	}

	if _, err := t.client.Write(seq); err != nil {
		return err
	}

	// Wait 500ms (else adapater crash)
	time.Sleep(500 * time.Millisecond)

	// Run reads from serial
	t.run()

	return nil
}

// Close a serial connection
func (t *USBCanAnalyzer) Close() error {
	if t.client == nil {
		return nil
	}

	// Stop reading serial port
	t.mutex.Lock()
	t.running = false
	t.mutex.Unlock()

	close(t.readChan)

	return t.client.Close()
}

// Write a frame to serial connection
func (t *USBCanAnalyzer) Write(frm *can.Frame) error {
	frmFullLen := 4 + int(frm.DLC) + 1
	data := make([]byte, frmFullLen)

	// 0xAA : adapter start of frame
	data[0] = 0xAA

	// DLC
	data[1] = 0xC0 | frm.DLC

	// Write arbitration id
	binary.LittleEndian.PutUint16(data[2:], uint16(frm.ArbitrationID))

	// Append data
	for i := 0; i < int(frm.DLC); i++ {
		data[i+4] = frm.Data[i]
	}

	// Adapater end of frame
	data[frmFullLen-1] = 0x55

	_, err := t.client.Write(data)
	return err
}

// ReadChan returns the read chan
func (t *USBCanAnalyzer) ReadChan() chan *can.Frame {
	return t.readChan
}
