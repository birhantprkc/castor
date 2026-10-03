package media

// Framing is where a container keeps the decoder configuration of the elementary streams it carries.
type Framing int

const (
	// FramingUnknown is the zero value: a container that has not declared how it frames its streams.
	FramingUnknown Framing = iota
	// FramingInBand repeats each track's decoder configuration inside the stream, ahead of every frame.
	FramingInBand
	FramingOutOfBand
)

func (f Framing) String() string {
	switch f {
	case FramingInBand:
		return "in-band"
	case FramingOutOfBand:
		return "out-of-band"
	default:
		return "unknown"
	}
}
