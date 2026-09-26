package codec

type VP9 struct{}

func (VP9) Name() string { return "vp9" }
func (VP9) EncoderArgs() []string {
	return []string{"libvpx-vp9", "-b:v", "600k", "-deadline", "realtime", "-cpu-used", "8", "-row-mt", "1"}
}
