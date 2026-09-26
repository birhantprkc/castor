package index

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func box(kind string, payload []byte) []byte {
	return append(append(binary.BigEndian.AppendUint32(nil, uint32(8+len(payload))), kind...), payload...)
}

// sidx is a version-0 index of subsegments of the given sizes, each a second at timescale 1000.
func sidx(sizes ...uint32) []byte {
	p := binary.BigEndian.AppendUint32(nil, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 1000)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(sizes)))
	for _, size := range sizes {
		p = binary.BigEndian.AppendUint32(p, size)
		p = binary.BigEndian.AppendUint32(p, 1000)
		p = binary.BigEndian.AppendUint32(p, 0x90000000)
	}
	return box("sidx", p)
}

func TestSubsegmentsCountFromTheEndOfTheIndexBoxWhereverItSits(t *testing.T) {
	styp := box("styp", bytes.Repeat([]byte{0}, 12))
	index := sidx(1000, 2000)
	refs, timescale, err := Sidx(append(styp, index...), 500)
	if err != nil {
		t.Fatal(err)
	}
	after := int64(500 + len(styp) + len(index))
	want := []Reference{{Offset: after, Length: 1000, Duration: 1000}, {Offset: after + 1000, Length: 2000, Duration: 1000}}
	if timescale != 1000 || len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Errorf("Sidx = %+v at %d, want %+v at 1000", refs, timescale, want)
	}
}

func TestAnIndexWithNoTimescaleIsRefused(t *testing.T) {
	index := sidx(1000)
	binary.BigEndian.PutUint32(index[16:], 0)
	if _, _, err := Sidx(index, 0); err == nil {
		t.Error("an index stating no timescale was read")
	}
}
