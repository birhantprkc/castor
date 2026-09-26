package codec

type FLAC struct{}

func (FLAC) Name() string          { return "flac" }
func (FLAC) EncoderArgs() []string { return []string{"flac"} }
