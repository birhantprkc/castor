package codec

// HEVC is tagged hvc1, the sample entry Apple and Cast receivers expect.
type HEVC struct{}

func (HEVC) Name() string { return "hevc" }
func (HEVC) EncoderArgs() []string {
	return []string{"libx265", "-preset", "ultrafast", "-tag:v", "hvc1", "-x265-params", "log-level=error"}
}
