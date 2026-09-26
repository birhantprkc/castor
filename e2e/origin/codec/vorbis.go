package codec

type Vorbis struct{}

func (Vorbis) Name() string          { return "vorbis" }
func (Vorbis) EncoderArgs() []string { return []string{"libvorbis"} }
