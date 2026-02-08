// Package sacp implements the Snapmaker SACP (Snapmaker Application Communication Protocol)
// for communicating with Snapmaker printers over TCP.
//
// Adapted from github.com/macdylan/sm2uploader (MIT License).
// Original author: https://github.com/kanocz
package sacp

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	DataLen = 60 * 1024 // chunk size for file uploads
	Port    = 8888
)

var (
	ErrInvalidPacket  = errors.New("data doesn't look like SACP packet")
	ErrInvalidVersion = errors.New("SACP version mismatch")
	ErrInvalidChksum  = errors.New("SACP checksum doesn't match data")
	ErrInvalidSize    = errors.New("SACP package is too short")
)

// Packet represents a SACP protocol packet.
type Packet struct {
	ReceiverID byte
	SenderID   byte
	Attribute  byte
	Sequence   uint16
	CommandSet byte
	CommandID  byte
	Data       []byte
}

// Encode serializes the packet into its wire format.
func (p Packet) Encode() []byte {
	result := make([]byte, 15+len(p.Data))

	result[0] = 0xAA
	result[1] = 0x55
	binary.LittleEndian.PutUint16(result[2:4], uint16(len(p.Data)+6+2))
	result[4] = 0x01
	result[5] = p.ReceiverID
	result[6] = headChksum(result[:6])
	result[7] = p.SenderID
	result[8] = p.Attribute
	binary.LittleEndian.PutUint16(result[9:11], p.Sequence)
	result[11] = p.CommandSet
	result[12] = p.CommandID

	if len(p.Data) > 0 {
		copy(result[13:], p.Data)
	}

	binary.LittleEndian.PutUint16(result[len(result)-2:], u16Chksum(result[7:], uint16(len(p.Data))+6))

	return result
}

// Decode deserializes a packet from its wire format.
func (p *Packet) Decode(data []byte) error {
	if len(data) < 13 {
		return ErrInvalidSize
	}
	if data[0] != 0xAA || data[1] != 0x55 {
		return ErrInvalidPacket
	}
	dataLen := binary.LittleEndian.Uint16(data[2:4])
	if dataLen != uint16(len(data)-7) {
		return ErrInvalidSize
	}
	if data[4] != 0x01 {
		return ErrInvalidVersion
	}
	if headChksum(data[:6]) != data[6] {
		return ErrInvalidChksum
	}
	if binary.LittleEndian.Uint16(data[len(data)-2:]) != u16Chksum(data[7:], dataLen-2) {
		return ErrInvalidChksum
	}

	p.ReceiverID = data[5]
	p.SenderID = data[7]
	p.Attribute = data[8]
	p.Sequence = binary.LittleEndian.Uint16(data[9:11])
	p.CommandSet = data[11]
	p.CommandID = data[12]
	p.Data = data[13 : len(data)-2]

	return nil
}

func headChksum(data []byte) byte {
	crc := byte(0)
	poly := byte(7)
	for i := 0; i < len(data); i++ {
		for j := 0; j < 8; j++ {
			bit := ((data[i] & 0xff) >> (7 - j) & 0x01) == 1
			c07 := (crc >> 7 & 0x01) == 1
			crc = crc << 1
			if (!c07 && bit) || (c07 && !bit) {
				crc ^= poly
			}
		}
	}
	return crc & 0xff
}

func u16Chksum(packageData []byte, length uint16) uint16 {
	checkNum := uint32(0)
	if length > 0 {
		for i := 0; i < int(length-1); i += 2 {
			checkNum += uint32((uint32(packageData[i])&0xff)<<8 | uint32(packageData[i+1])&0xff)
			checkNum &= 0xffffffff
		}
		if length%2 != 0 {
			checkNum += uint32(packageData[length-1])
		}
	}
	for checkNum > 0xFFFF {
		checkNum = ((checkNum >> 16) & 0xFFFF) + (checkNum & 0xFFFF)
	}
	checkNum = ^checkNum
	return uint16(checkNum & 0xFFFF)
}

func writeString(w io.Writer, s string) {
	binary.Write(w, binary.LittleEndian, uint16(len(s)))
	w.Write([]byte(s))
}

func writeBytes(w io.Writer, s []byte) {
	binary.Write(w, binary.LittleEndian, uint16(len(s)))
	w.Write(s)
}

func writeLE[T any](w io.Writer, u T) {
	binary.Write(w, binary.LittleEndian, u)
}

// sequenceMu protects the global sequence counter.
var (
	sequenceMu sync.Mutex
	sequence   uint16 = 2
)

func nextSequence() uint16 {
	sequenceMu.Lock()
	defer sequenceMu.Unlock()
	sequence++
	return sequence
}

// Connect establishes a SACP TCP connection to a printer at the given IP.
func Connect(ip string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp4", ip+":8888", timeout)
	if err != nil {
		return nil, err
	}

	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err = conn.Write(Packet{
		ReceiverID: 2,
		SenderID:   0,
		Attribute:  0,
		Sequence:   1,
		CommandSet: 0x01,
		CommandID:  0x05,
		Data: []byte{
			11, 0, 's', 'm', '2', 'u', 'p', 'l', 'o', 'a', 'd', 'e', 'r',
			0, 0,
			0, 0,
		},
	}.Encode())

	if err != nil {
		conn.Close()
		return nil, err
	}

	for {
		p, err := Read(conn, timeout)
		if err != nil || p == nil {
			conn.Close()
			return nil, err
		}

		if p.CommandSet == 1 && p.CommandID == 5 {
			break
		}
	}

	return conn, nil
}

// Read reads a single SACP packet from the connection.
func Read(conn net.Conn, timeout time.Duration) (*Packet, error) {
	var buf [DataLen + 15]byte

	conn.SetReadDeadline(time.Now().Add(timeout))

	n, err := conn.Read(buf[:4])
	if err != nil {
		return nil, err
	}
	if n != 4 {
		return nil, ErrInvalidSize
	}

	dataLen := binary.LittleEndian.Uint16(buf[2:4])
	n, err = conn.Read(buf[4 : dataLen+7])
	if err != nil {
		return nil, err
	}
	if n != int(dataLen+3) {
		return nil, ErrInvalidSize
	}

	var p Packet
	err = p.Decode(buf[:dataLen+7])
	return &p, err
}

// SendCommand sends a SACP command and waits for the matching response.
func SendCommand(conn net.Conn, commandSet uint8, commandID uint8, data bytes.Buffer, timeout time.Duration) error {
	seq := nextSequence()

	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(Packet{
		ReceiverID: 1,
		SenderID:   0,
		Attribute:  0,
		Sequence:   seq,
		CommandSet: commandSet,
		CommandID:  commandID,
		Data:       data.Bytes(),
	}.Encode())

	if err != nil {
		return err
	}

	for {
		conn.SetReadDeadline(time.Now().Add(timeout))
		p, err := Read(conn, timeout)
		if err != nil {
			return err
		}

		if p.Sequence == seq && p.CommandSet == commandSet && p.CommandID == commandID {
			if len(p.Data) == 1 && p.Data[0] == 0 {
				return nil
			}
		}
	}
}

// Disconnect sends the SACP disconnect command.
func Disconnect(conn net.Conn, timeout time.Duration) error {
	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(Packet{
		ReceiverID: 2,
		SenderID:   0,
		Attribute:  0,
		Sequence:   1,
		CommandSet: 0x01,
		CommandID:  0x06,
		Data:       []byte{},
	}.Encode())
	return err
}

// SetToolTemperature sets the extruder temperature via SACP.
func SetToolTemperature(conn net.Conn, toolID uint8, temperature uint16, timeout time.Duration) error {
	data := bytes.Buffer{}
	data.WriteByte(0x08)
	data.WriteByte(toolID)
	writeLE(&data, temperature)
	return SendCommand(conn, 0x10, 0x02, data, timeout)
}

// SetBedTemperature sets the heated bed temperature via SACP.
func SetBedTemperature(conn net.Conn, toolID uint8, temperature uint16, timeout time.Duration) error {
	data := bytes.Buffer{}
	data.WriteByte(0x05)
	data.WriteByte(toolID)
	writeLE(&data, temperature)
	return SendCommand(conn, 0x14, 0x02, data, timeout)
}

// Home sends a home-all-axes command via SACP.
func Home(conn net.Conn, timeout time.Duration) error {
	data := bytes.Buffer{}
	data.WriteByte(0x00)
	return SendCommand(conn, 0x01, 0x35, data, timeout)
}

// StartUpload uploads gcode data to the printer via the SACP file transfer protocol.
func StartUpload(conn net.Conn, filename string, gcode []byte, timeout time.Duration) error {
	packageCount := uint16((len(gcode) / DataLen) + 1)
	md5hash := md5.Sum(gcode)

	data := bytes.Buffer{}
	writeString(&data, filename)
	writeLE(&data, uint32(len(gcode)))
	writeLE(&data, packageCount)
	writeString(&data, hex.EncodeToString(md5hash[:]))

	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(Packet{
		ReceiverID: 2,
		SenderID:   0,
		Attribute:  0,
		Sequence:   1,
		CommandSet: 0xb0,
		CommandID:  0x00,
		Data:       data.Bytes(),
	}.Encode())

	if err != nil {
		return err
	}

	for {
		conn.SetReadDeadline(time.Now().Add(timeout))
		p, err := Read(conn, 10*time.Second)
		if err != nil {
			return err
		}
		if p == nil {
			return ErrInvalidSize
		}

		switch {
		case p.CommandSet == 0xb0 && p.CommandID == 0:
			// Acknowledgement, continue

		case p.CommandSet == 0xb0 && p.CommandID == 1:
			// Printer requesting a data chunk
			if len(p.Data) < 4 {
				return ErrInvalidSize
			}
			md5Len := binary.LittleEndian.Uint16(p.Data[:2])
			if len(p.Data) < 2+int(md5Len)+2 {
				return ErrInvalidSize
			}

			pkgRequested := binary.LittleEndian.Uint16(p.Data[2+md5Len : 2+md5Len+2])
			var pkgData []byte

			if pkgRequested == packageCount-1 {
				pkgData = gcode[DataLen*int(pkgRequested):]
			} else {
				pkgData = gcode[DataLen*int(pkgRequested) : DataLen*int(pkgRequested+1)]
			}

			chunkBuf := bytes.Buffer{}
			chunkBuf.WriteByte(0)
			writeString(&chunkBuf, hex.EncodeToString(md5hash[:]))
			writeLE(&chunkBuf, pkgRequested)
			writeBytes(&chunkBuf, pkgData)

			perc := float64(pkgRequested+1) / float64(packageCount) * 100.0
			log.Printf("  SACP upload: %.1f%%", perc)

			conn.SetWriteDeadline(time.Now().Add(timeout))
			_, err := conn.Write(Packet{
				ReceiverID: 2,
				SenderID:   0,
				Attribute:  1,
				Sequence:   p.Sequence,
				CommandSet: 0xb0,
				CommandID:  0x01,
				Data:       chunkBuf.Bytes(),
			}.Encode())

			if err != nil {
				return err
			}

		case p.CommandSet == 0xb0 && p.CommandID == 2:
			// Upload complete
			if len(p.Data) == 1 && p.Data[0] == 0 {
				return nil
			}
			log.Printf("Unexpected upload completion data: %v", p.Data)

		default:
			continue
		}
	}
}
