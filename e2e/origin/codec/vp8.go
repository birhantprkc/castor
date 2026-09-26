package codec

type VP8 struct{}

func (VP8) Name() string { return "vp8" }
func (VP8) EncoderArgs() []string {
	return []string{"libvpx", "-b:v", "600k", "-deadline", "realtime", "-cpu-used", "8"}
}
