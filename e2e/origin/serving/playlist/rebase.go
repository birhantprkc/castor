package playlist

import (
	"errors"
	"fmt"
)

const (
	tsPacket = 188
	// ptsWrap is where the 33-bit PTS, DTS and PCR base roll over.
	ptsWrap = 1 << 33
)

// rebase moves every PES timestamp and PCR base of a TS back by ticks of the 90 kHz clock, in place.
func rebase(ts []byte, ticks uint64) error {
	if len(ts) == 0 || len(ts)%tsPacket != 0 || ts[0] != 0x47 {
		return errors.New("restarts-at: the segment is not an MPEG-TS")
	}
	back := func(v uint64) uint64 { return (v + ptsWrap - ticks%ptsWrap) % ptsWrap }
	for off := 0; off < len(ts); off += tsPacket {
		pkt := ts[off : off+tsPacket]
		if pkt[0] != 0x47 {
			return fmt.Errorf("restarts-at: lost TS sync at byte %d", off)
		}
		control, payload := pkt[3]>>4&3, 4
		if control&2 != 0 {
			length := int(pkt[4])
			if length > 0 && pkt[5]&0x10 != 0 && length >= 7 {
				writePCR(pkt[6:], back(readPCR(pkt[6:])))
			}
			payload += 1 + length
		}
		if control&1 == 0 || pkt[1]&0x40 == 0 || payload+9 > tsPacket {
			continue
		}
		pes := pkt[payload:]
		// Only a PES start code with the optional header carries timestamps; PSI sections begin with a pointer field.
		if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[6]&0xc0 != 0x80 {
			continue
		}
		flags := pes[7] >> 6
		if flags&2 != 0 && len(pes) >= 14 {
			writeTimestamp(pes[9:], back(readTimestamp(pes[9:])))
		}
		if flags == 3 && len(pes) >= 19 {
			writeTimestamp(pes[14:], back(readTimestamp(pes[14:])))
		}
	}
	return nil
}

func readTimestamp(b []byte) uint64 {
	return uint64(b[0]>>1&7)<<30 | uint64(b[1])<<22 | uint64(b[2]>>1)<<15 | uint64(b[3])<<7 | uint64(b[4]>>1)
}

// writeTimestamp keeps the prefix nibble and the marker bits.
func writeTimestamp(b []byte, v uint64) {
	b[0] = b[0]&0xf1 | byte(v>>30&7)<<1
	b[1] = byte(v >> 22)
	b[2] = b[2]&1 | byte(v>>15&0x7f)<<1
	b[3] = byte(v >> 7)
	b[4] = b[4]&1 | byte(v&0x7f)<<1
}

func readPCR(b []byte) uint64 {
	return uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 | uint64(b[3])<<1 | uint64(b[4]>>7)
}

// writePCR keeps the reserved bits and the 27 MHz extension.
func writePCR(b []byte, v uint64) {
	b[0], b[1], b[2], b[3] = byte(v>>25), byte(v>>17), byte(v>>9), byte(v>>1)
	b[4] = b[4]&0x7f | byte(v&1)<<7
}
