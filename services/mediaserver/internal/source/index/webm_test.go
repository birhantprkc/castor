package index

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// Matroska element ids the WebM fixtures are written with.
const (
	ebmlSegment       = 0x18538067
	ebmlInfo          = 0x1549A966
	ebmlTimecodeScale = 0x2AD7B1
	ebmlDuration      = 0x4489
	ebmlCues          = 0x1C53BB6B
	ebmlCuePoint      = 0xBB
	ebmlCueTime       = 0xB3
	ebmlCueTrackPos   = 0xB7
	ebmlClusterAt     = 0xF1
)

// ebml is one element: its id as written, an eight-byte size, and its children.
func ebml(id uint32, children ...[]byte) []byte {
	payload := bytes.Join(children, nil)
	var head []byte
	for shift := 24; shift >= 0; shift -= 8 {
		if b := byte(id >> shift); b != 0 || len(head) > 0 {
			head = append(head, b)
		}
	}
	size := binary.BigEndian.AppendUint64(nil, uint64(len(payload)))
	size[0] = 0x01
	return append(append(head, size...), payload...)
}

func number(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }

// cue points at the cluster at a position counted from the Segment's data.
func cue(time, at uint64) []byte {
	return ebml(ebmlCuePoint, ebml(ebmlCueTime, number(time)), ebml(ebmlCueTrackPos, ebml(ebmlClusterAt, number(at))))
}

func TestAWebMIndexCutsTheFileAtEachCluster(t *testing.T) {
	info := ebml(ebmlInfo, ebml(ebmlTimecodeScale, number(1_000_000)), ebml(ebmlDuration, binary.BigEndian.AppendUint64(nil, math.Float64bits(3000))))
	clusters := [][]byte{bytes.Repeat([]byte{1}, 100), bytes.Repeat([]byte{2}, 200), bytes.Repeat([]byte{3}, 50)}
	// Positions count from the Segment's data, which starts after its header.
	cues := ebml(ebmlCues, cue(0, uint64(len(info))), cue(1000, uint64(len(info)+100)), cue(2000, uint64(len(info)+300)))
	segment := ebml(ebmlSegment, info, bytes.Join(clusters, nil), cues)
	file := append(ebml(0x1A45DFA3), segment...)
	data := int64(len(file) - len(segment) + 12)
	at := int64(len(file) - len(cues))

	refs, timescale, err := Cues(file[:data+int64(len(info))], file[at:], at)
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{
		{Offset: data + int64(len(info)), Length: 100, Duration: 1000},
		{Offset: data + int64(len(info)) + 100, Length: 200, Duration: 1000},
		{Offset: data + int64(len(info)) + 300, Length: 50, Duration: 1000},
	}
	if timescale != 1000 || len(refs) != len(want) {
		t.Fatalf("Cues = %+v at %d ticks a second, want %+v at 1000", refs, timescale, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("cluster %d = %+v, want %+v", i, refs[i], want[i])
		}
	}
}

func TestTracksCueingOneClusterMakeOneSubsegment(t *testing.T) {
	info := ebml(ebmlInfo, ebml(ebmlTimecodeScale, number(1_000_000)))
	first := uint64(len(info))
	cues := ebml(ebmlCues, cue(0, first), cue(0, first), cue(1000, first+100), cue(1000, first+100))
	segment := ebml(ebmlSegment, info, bytes.Repeat([]byte{1}, 300), cues)
	file := append(ebml(0x1A45DFA3), segment...)
	at := int64(len(file) - len(cues))
	refs, _, err := Cues(file[:len(file)-len(segment)+12+len(info)], file[at:], at)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Length != 100 || refs[1].Length != 200 {
		t.Errorf("Cues = %+v, want two clusters of 100 and 200 bytes", refs)
	}
}
