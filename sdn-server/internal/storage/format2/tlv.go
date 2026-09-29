package format2

import "encoding/binary"

// tlv builds the engine's configuration blocks: [u16 tag][u32 len][bytes].
type tlv []byte

func (t tlv) bytes(tag uint16, v []byte) tlv {
	t = binary.LittleEndian.AppendUint16(t, tag)
	t = binary.LittleEndian.AppendUint32(t, uint32(len(v)))
	return append(t, v...)
}

func (t tlv) u8(tag uint16, v bool) tlv {
	b := byte(0)
	if v {
		b = 1
	}
	return t.bytes(tag, []byte{b})
}

func (t tlv) u32(tag uint16, v uint32) tlv {
	return t.bytes(tag, binary.LittleEndian.AppendUint32(nil, v))
}

func (t tlv) u64(tag uint16, v uint64) tlv {
	return t.bytes(tag, binary.LittleEndian.AppendUint64(nil, v))
}
