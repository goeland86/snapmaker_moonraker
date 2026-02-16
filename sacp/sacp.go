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
	"fmt"
	"io"
	"log"
	"math"
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
			24, 0, 'M', 'o', 'o', 'n', 'r', 'a', 'k', 'e', 'r', ' ',
			'R', 'e', 'm', 'o', 't', 'e', ' ', 'C', 'o', 'n', 't', 'r', 'o', 'l',
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

	_, err := io.ReadFull(conn, buf[:4])
	if err != nil {
		return nil, err
	}

	if buf[0] != 0xAA || buf[1] != 0x55 {
		return nil, ErrInvalidPacket
	}

	dataLen := binary.LittleEndian.Uint16(buf[2:4])
	totalLen := int(dataLen) + 7
	if totalLen > len(buf) {
		return nil, ErrInvalidSize
	}

	_, err = io.ReadFull(conn, buf[4:totalLen])
	if err != nil {
		return nil, err
	}

	var p Packet
	err = p.Decode(buf[:totalLen])
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

// ExecuteGCode sends a G-code command via SACP (command set 0x01, command ID 0x02)
// and returns the response string.
func ExecuteGCode(conn net.Conn, gcode string, timeout time.Duration) (string, error) {
	seq := nextSequence()

	// Build the data payload: length-prefixed string.
	data := bytes.Buffer{}
	writeString(&data, gcode)

	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(Packet{
		ReceiverID: 1,
		SenderID:   0,
		Attribute:  0,
		Sequence:   seq,
		CommandSet: 0x01,
		CommandID:  0x02,
		Data:       data.Bytes(),
	}.Encode())

	if err != nil {
		return "", err
	}

	for {
		p, err := Read(conn, timeout)
		if err != nil {
			return "", err
		}

		if p.Sequence == seq && p.CommandSet == 0x01 && p.CommandID == 0x02 {
			log.Printf("SACP GCode response: seq=%d dataLen=%d data=%x", p.Sequence, len(p.Data), p.Data)
			// Response data: first byte is result (0=success), rest is response string.
			if len(p.Data) < 1 {
				return "", nil
			}
			if p.Data[0] != 0 {
				return "", fmt.Errorf("gcode execution failed with result code %d", p.Data[0])
			}
			if len(p.Data) > 1 {
				// Parse response string (length-prefixed or raw).
				return string(p.Data[1:]), nil
			}
			return "", nil
		}
	}
}

// Subscribe sends a SACP subscription request.
// The printer will then periodically send packets with the given commandSet/commandID.
func Subscribe(conn net.Conn, commandSet uint8, commandID uint8, intervalMs uint16, timeout time.Duration) error {
	seq := nextSequence()

	data := bytes.Buffer{}
	writeLE(&data, intervalMs)

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

	// Read the acknowledgment.
	for {
		p, err := Read(conn, timeout)
		if err != nil {
			return err
		}
		if p.Sequence == seq && p.CommandSet == commandSet && p.CommandID == commandID {
			return nil
		}
	}
}

// ParseExtruderInfo parses nozzle query/subscription data (CommandSet 0x10, CommandID 0xa0).
// Format: 3-byte header + 17-byte extruder records.
//
// Header (3 bytes):
//
//	byte[0]: context-dependent (key for push, result for ACK)
//	byte[1]: context-dependent (head_status for push, key for ACK)
//	byte[2]: extruder_count
//
// Per-extruder record (17 bytes):
//
//	index(1) + filament_status(1) + filament_enable(1) + is_available(1) + type(1)
//	+ diameter(int32 LE, 4) + cur_temp(int32 LE, 4) + target_temp(int32 LE, 4)
//
// Temperatures are int32 LE in millidegrees (÷1000 for °C).
func ParseExtruderInfo(data []byte) (extruders []ExtruderData) {
	if len(data) < 3 {
		return nil
	}
	headID := int(data[1]) // 0=T0 (left), 1=T1 (right) on J1S
	count := int(data[2])
	offset := 3
	const recordSize = 17

	for i := 0; i < count && offset+recordSize <= len(data); i++ {
		e := ExtruderData{
			Index:  int(data[offset]),
			HeadID: headID,
		}
		raw := int32(binary.LittleEndian.Uint32(data[offset+9 : offset+13]))
		e.CurrentTemp = float64(raw) / 1000.0
		raw = int32(binary.LittleEndian.Uint32(data[offset+13 : offset+17]))
		e.TargetTemp = float64(raw) / 1000.0
		extruders = append(extruders, e)
		offset += recordSize
	}
	return
}

// ParseBedInfo parses heated bed query/subscription data (CommandSet 0x14, CommandID 0xa0).
// Format: 3-byte header + 7-byte zone records.
//
// Header (3 bytes):
//
//	byte[0]: context-dependent (key for push, result for ACK)
//	byte[1]: key (e.g. 0x90 on J1S)
//	byte[2]: zone_count
//
// Per-zone record (7 bytes):
//
//	zone_index(1) + cur_temp(int32 LE, 4) + target_temp(int16 LE, 2)
//
// cur_temp is int32 LE in millidegrees (÷1000 for °C).
// target_temp is int16 LE (unit TBD - treated as whole degrees for now).
func ParseBedInfo(data []byte) (zones []BedZoneData) {
	if len(data) < 3 {
		return nil
	}
	count := int(data[2])
	offset := 3
	const recordSize = 7

	for i := 0; i < count && offset+recordSize <= len(data); i++ {
		z := BedZoneData{
			Index: int(data[offset]),
		}
		raw := int32(binary.LittleEndian.Uint32(data[offset+1 : offset+5]))
		z.CurrentTemp = float64(raw) / 1000.0
		rawTarget := int16(binary.LittleEndian.Uint16(data[offset+5 : offset+7]))
		z.TargetTemp = float64(rawTarget)
		zones = append(zones, z)
		offset += recordSize
	}
	return
}

// ExtruderData holds parsed extruder temperature info.
type ExtruderData struct {
	Index       int
	HeadID      int // from header byte[1]: 0=T0 (left), 1=T1 (right) on J1S
	CurrentTemp float64
	TargetTemp  float64
}

// BedZoneData holds parsed bed zone temperature info.
type BedZoneData struct {
	Index       int
	CurrentTemp float64
	TargetTemp  float64
}

// MachineStatus represents the printer's system status from the heartbeat.
type MachineStatus uint8

const (
	MachineStatusIdle       MachineStatus = 0
	MachineStatusStarting   MachineStatus = 1
	MachineStatusPrinting   MachineStatus = 2
	MachineStatusPausing    MachineStatus = 3
	MachineStatusPaused     MachineStatus = 4
	MachineStatusStopping   MachineStatus = 5
	MachineStatusStopped    MachineStatus = 6
	MachineStatusFinishing  MachineStatus = 7
	MachineStatusCompleted  MachineStatus = 8
	MachineStatusRecovering MachineStatus = 9
	MachineStatusResuming   MachineStatus = 10
)

func (s MachineStatus) String() string {
	names := [...]string{
		"IDLE", "STARTING", "PRINTING", "PAUSING", "PAUSED",
		"STOPPING", "STOPPED", "FINISHING", "COMPLETED",
		"RECOVERING", "RESUMING",
	}
	if int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("UNKNOWN(%d)", s)
}

// ParseHeartbeat parses heartbeat subscription data (CommandSet 0x01, CommandID 0xA0).
// Format: byte[0]=result, byte[1]=status.
func ParseHeartbeat(data []byte) (MachineStatus, error) {
	if len(data) < 2 {
		return 0, fmt.Errorf("heartbeat data too short: %d bytes", len(data))
	}
	return MachineStatus(data[1]), nil
}

// ParseCurrentLine parses print line subscription data (CommandSet 0xAC, CommandID 0xA0).
// Format: byte[0]=result, byte[1-4]=current_line (uint32 LE).
func ParseCurrentLine(data []byte) (uint32, error) {
	if len(data) < 5 {
		return 0, fmt.Errorf("current line data too short: %d bytes", len(data))
	}
	return binary.LittleEndian.Uint32(data[1:5]), nil
}

// ParsePrintTime parses elapsed time subscription data (CommandSet 0xAC, CommandID 0xA5).
// Format: byte[0]=result, byte[1-4]=elapsed_seconds (uint32 LE).
func ParsePrintTime(data []byte) (uint32, error) {
	if len(data) < 5 {
		return 0, fmt.Errorf("print time data too short: %d bytes", len(data))
	}
	return binary.LittleEndian.Uint32(data[1:5]), nil
}

// FanData holds parsed fan info from subscription (CommandSet 0x10, CommandID 0xA3).
type FanData struct {
	HeadID   int
	FanIndex int
	FanType  int   // 0=part fan, 2=hotend fan
	Speed    uint8 // 0-255
}

// ParseFanInfo parses fan subscription data.
// Format: byte[0]=result, byte[1]=key/head_id, byte[2]=fan_count,
// then fan_count * 3 bytes: [fan_index, fan_type, fan_speed].
func ParseFanInfo(data []byte) ([]FanData, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("fan data too short: %d bytes", len(data))
	}
	headID := int(data[1])
	fanCount := int(data[2])
	offset := 3
	var fans []FanData
	for i := 0; i < fanCount && offset+3 <= len(data); i++ {
		fans = append(fans, FanData{
			HeadID:   headID,
			FanIndex: int(data[offset]),
			FanType:  int(data[offset+1]),
			Speed:    data[offset+2],
		})
		offset += 3
	}
	return fans, nil
}

// CoordinateData holds parsed position info.
type CoordinateData struct {
	Homed   bool
	X, Y, Z float64
}

// ParseCoordinateInfo parses coordinate data (CommandSet 0x01, CommandID 0x30).
// Format: byte[0]=result, byte[1]=homed(0=yes), byte[2]=coord_system_id,
// byte[3]=is_origin_offset, byte[4]=axis_count,
// then axis_count * 5 bytes: [axis_id(1) + position_um(int32 LE)].
func ParseCoordinateInfo(data []byte) (CoordinateData, error) {
	var cd CoordinateData
	if len(data) < 5 {
		return cd, fmt.Errorf("coordinate data too short: %d bytes", len(data))
	}

	cd.Homed = data[1] == 0 // 0 = homed
	axisCount := int(data[4])
	offset := 5

	for i := 0; i < axisCount && offset+5 <= len(data); i++ {
		axis := data[offset]
		value := float64(int32(binary.LittleEndian.Uint32(data[offset+1:offset+5]))) / 1000.0
		switch axis {
		case 0:
			cd.X = value // X1
		case 1:
			cd.Y = value // Y1
		case 2:
			cd.Z = value // Z1
		}
		offset += 5
	}

	return cd, nil
}

// PrintFileInfo holds parsed file info for the current print job.
type PrintFileInfo struct {
	MD5           string
	Filename      string
	TotalLines    uint32
	EstimatedTime uint32 // seconds
}

// ParseFileInfo parses file info query response (CommandSet 0xAC, CommandID 0x00).
// Format: byte[0]=result, then length-prefixed md5, then length-prefixed filename.
func ParseFileInfo(data []byte) (PrintFileInfo, error) {
	if len(data) < 3 {
		return PrintFileInfo{}, fmt.Errorf("file info too short")
	}
	if data[0] != 0 {
		return PrintFileInfo{}, fmt.Errorf("file info query failed: code %d", data[0])
	}
	offset := 1
	// MD5 (length-prefixed uint16 LE)
	if offset+2 > len(data) {
		return PrintFileInfo{}, fmt.Errorf("missing md5 length")
	}
	md5Len := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+md5Len > len(data) {
		return PrintFileInfo{}, fmt.Errorf("md5 truncated")
	}
	md5Str := string(data[offset : offset+md5Len])
	offset += md5Len

	// Filename (length-prefixed uint16 LE)
	if offset+2 > len(data) {
		return PrintFileInfo{}, fmt.Errorf("missing filename length")
	}
	nameLen := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+nameLen > len(data) {
		return PrintFileInfo{}, fmt.Errorf("filename truncated")
	}
	filename := string(data[offset : offset+nameLen])

	return PrintFileInfo{MD5: md5Str, Filename: filename}, nil
}

// ParsePrintingFileInfo parses printing file info response (CommandSet 0xAC, CommandID 0x1A).
// Format: byte[0]=result, then length-prefixed filename, uint32 total_lines, uint32 estimated_time.
func ParsePrintingFileInfo(data []byte) (PrintFileInfo, error) {
	if len(data) < 3 {
		return PrintFileInfo{}, fmt.Errorf("printing file info too short")
	}
	if data[0] != 0 {
		return PrintFileInfo{}, fmt.Errorf("printing file info failed: code %d", data[0])
	}
	offset := 1
	// Filename (length-prefixed uint16 LE)
	if offset+2 > len(data) {
		return PrintFileInfo{}, fmt.Errorf("missing filename length")
	}
	nameLen := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+nameLen > len(data) {
		return PrintFileInfo{}, fmt.Errorf("filename truncated")
	}
	filename := string(data[offset : offset+nameLen])
	offset += nameLen

	fi := PrintFileInfo{Filename: filename}
	if offset+4 <= len(data) {
		fi.TotalLines = binary.LittleEndian.Uint32(data[offset : offset+4])
		offset += 4
	}
	if offset+4 <= len(data) {
		fi.EstimatedTime = binary.LittleEndian.Uint32(data[offset : offset+4])
	}
	return fi, nil
}

func readFloat32LE(b []byte) float32 {
	bits := binary.LittleEndian.Uint32(b)
	return math.Float32frombits(bits)
}

// WritePacket writes a SACP command packet to the controller (ReceiverID=1).
func WritePacket(conn net.Conn, commandSet, commandID byte, data []byte, timeout time.Duration) (uint16, error) {
	return WritePacketTo(conn, 1, commandSet, commandID, data, timeout)
}

// WritePacketTo writes a SACP command packet to a specific receiver.
// ReceiverID 1 = controller, 2 = screen.
func WritePacketTo(conn net.Conn, receiverID byte, commandSet, commandID byte, data []byte, timeout time.Duration) (uint16, error) {
	seq := nextSequence()
	conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(Packet{
		ReceiverID: receiverID,
		SenderID:   0,
		Attribute:  0,
		Sequence:   seq,
		CommandSet: commandSet,
		CommandID:  commandID,
		Data:       data,
	}.Encode())
	return seq, err
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
