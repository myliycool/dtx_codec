package dtx

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/danielpaulus/nskeyedarchiver"
)

type DtxMessage struct {
	Fragments         uint16
	FragmentIndex     uint16
	MessageLength     int
	Identifier        int
	ConversationIndex int
	ChannelCode       int
	ExpectsReply      bool
	PayloadHeader     DtxPayloadHeader
	Payload           []interface{}
	AuxiliaryHeader   AuxiliaryHeader
	Auxiliary         DtxPrimitiveDictionary
	rawBytes          []byte
	fragmentBytes     []byte
}

//16 Bytes
type DtxPayloadHeader struct {
	MessageType        int
	AuxiliaryLength    int
	TotalPayloadLength int
	Flags              int
}

//This header can actually be completely ignored. We do not need to care about the buffer size
//And we already know the AuxiliarySize. The other two ints seem to be always 0 anyway. Could
//also be that Buffer and Aux Size are Uint64
type AuxiliaryHeader struct {
	BufferSize    uint32
	Unknown       uint32
	AuxiliarySize uint32
	Unknown2      uint32
}

func (a AuxiliaryHeader) String() string {
	return fmt.Sprintf("BufSiz:%d Unknown:%d AuxSiz:%d Unknown2:%d", a.BufferSize, a.Unknown, a.AuxiliarySize, a.Unknown2)
}

func (d DtxMessage) String() string {
	var e = ""
	if d.ExpectsReply {
		e = "e"
	}
	msgtype := fmt.Sprintf("Unknown:%d", d.PayloadHeader.MessageType)
	if knowntype, ok := messageTypeLookup[d.PayloadHeader.MessageType]; ok {
		msgtype = knowntype
	}

	return fmt.Sprintf("i%d.%d%s c%d t:%s mlen:%d aux_len%d paylen%d", d.Identifier, d.ConversationIndex, e, d.ChannelCode, msgtype,
		d.MessageLength, d.PayloadHeader.AuxiliaryLength, d.PayloadLength())
}

func (d DtxMessage) StringDebug() string {
	if Ack == d.PayloadHeader.MessageType {
		return d.String()
	}
	payload := "none"
	if d.HasPayload() {
		b, _ := json.Marshal(d.Payload[0])
		payload = string(b)
	}
	if d.HasAuxiliary() {
		return fmt.Sprintf("auxheader:%s\naux:%s\npayload: %s \nrawbytes:%x", d.AuxiliaryHeader, d.Auxiliary, payload, d.rawBytes)
	}
	return fmt.Sprintf("no aux,payload: %s \nrawbytes:%x", payload, d.rawBytes)
}
func (d DtxMessage) parsePayloadBytes(messageBytes []byte) ([]interface{}, error) {
	offset := 0
	if d.HasAuxiliary() && d.HasPayload() {
		offset = 48 + d.PayloadHeader.AuxiliaryLength
	}
	if !d.HasAuxiliary() && d.HasPayload() {
		offset = 48
	}

	return nskeyedarchiver.Unarchive(messageBytes[offset:])
}

func (d DtxMessage) PayloadLength() int {
	return d.PayloadHeader.TotalPayloadLength - d.PayloadHeader.AuxiliaryLength
}

func (d DtxMessage) HasAuxiliary() bool {
	return d.PayloadHeader.AuxiliaryLength > 0
}

func (d DtxMessage) HasPayload() bool {
	return d.PayloadLength() > 0
}

const (
	MethodInvocationWithExpectedReply    = 0x3
	MethodinvocationWithoutExpectedReply = 0x2
	Ack                                  = 0x0
)

var messageTypeLookup = map[int]string{
	MethodInvocationWithExpectedReply:    `rpc_void`,
	MethodinvocationWithoutExpectedReply: `rpc_asking_reply`,
	Ack:                                  `Ack`,
}

const (
	DtxMessageMagic uint32 = 0x795B3D1F
	DtxHeaderLength uint32 = 32
	DtxReservedBits uint32 = 0x0
)

//This message is only 32 bytes long
func (d DtxMessage) IsFirstFragment() bool {
	return d.Fragments > 1 && d.FragmentIndex == 0
}

func (d DtxMessage) IsLastFragment() bool {
	return d.Fragments-d.FragmentIndex == 1
}

func (d DtxMessage) IsFragment() bool {
	return d.Fragments > 1
}

//Indicates whether the message you call this on, is the first part of a fragmented message, and if otherMessage is a subsequent fragment
func (d DtxMessage) MessageIsFirstFragmentFor(otherMessage DtxMessage) bool {
	if !d.IsFirstFragment() {
		panic("Illegal state")
	}
	return d.Identifier == otherMessage.Identifier && d.Fragments == otherMessage.Fragments && otherMessage.FragmentIndex > 0
}

func Decode(messageBytes []byte) (DtxMessage, []byte, error) {

	if binary.BigEndian.Uint32(messageBytes) != DtxMessageMagic {
		return DtxMessage{}, make([]byte, 0), fmt.Errorf("Wrong Magic: %x", messageBytes[0:4])
	}
	if binary.LittleEndian.Uint32(messageBytes[4:]) != DtxHeaderLength {
		return DtxMessage{}, make([]byte, 0), fmt.Errorf("Incorrect Header length, should be 32: %x", messageBytes[4:8])
	}
	result := DtxMessage{}
	result.FragmentIndex = binary.LittleEndian.Uint16(messageBytes[8:])
	result.Fragments = binary.LittleEndian.Uint16(messageBytes[10:])
	result.MessageLength = int(binary.LittleEndian.Uint32(messageBytes[12:]))
	result.Identifier = int(binary.LittleEndian.Uint32(messageBytes[16:]))
	result.ConversationIndex = int(binary.LittleEndian.Uint32(messageBytes[20:]))
	result.ChannelCode = int(binary.LittleEndian.Uint32(messageBytes[24:]))

	result.ExpectsReply = binary.LittleEndian.Uint32(messageBytes[28:]) == uint32(1)

	if result.IsFirstFragment() {
		return result, messageBytes[32:], nil
	}
	if result.IsFragment() {
		result.fragmentBytes = messageBytes[32 : result.MessageLength+32]
		return result, messageBytes[result.MessageLength+32:], nil
	}
	ph, err := parsePayloadHeader(messageBytes[32:48])
	if err != nil {
		return DtxMessage{}, make([]byte, 0), err
	}
	result.PayloadHeader = ph

	if result.HasAuxiliary() {
		header, err := parseAuxiliaryHeader(messageBytes[48:64])
		if err != nil {
			return DtxMessage{}, make([]byte, 0), err
		}
		result.AuxiliaryHeader = header
		auxBytes := messageBytes[64 : 48+result.PayloadHeader.AuxiliaryLength]
		result.Auxiliary = decodeAuxiliary(auxBytes)
	}

	totalMessageLength := result.MessageLength + int(DtxHeaderLength)
	result.rawBytes = messageBytes[:totalMessageLength]
	if result.HasPayload() {
		payload, err := result.parsePayloadBytes(result.rawBytes)
		if err != nil {
			return DtxMessage{}, make([]byte, 0), err
		}
		result.Payload = payload
	}

	remainingBytes := messageBytes[totalMessageLength:]
	return result, remainingBytes, nil
}

func parseAuxiliaryHeader(headerBytes []byte) (AuxiliaryHeader, error) {
	r := bytes.NewReader(headerBytes)
	var result AuxiliaryHeader
	err := binary.Read(r, binary.LittleEndian, &result)
	if err != nil {
		return result, err
	}
	return result, nil
}

func parsePayloadHeader(messageBytes []byte) (DtxPayloadHeader, error) {
	result := DtxPayloadHeader{}
	result.MessageType = int(binary.LittleEndian.Uint32(messageBytes))
	result.AuxiliaryLength = int(binary.LittleEndian.Uint32(messageBytes[4:]))
	result.TotalPayloadLength = int(binary.LittleEndian.Uint32(messageBytes[8:]))
	result.Flags = int(binary.LittleEndian.Uint32(messageBytes[12:]))

	return result, nil
}
