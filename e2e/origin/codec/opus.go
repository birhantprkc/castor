package codec

type Opus struct{}

func (Opus) Name() string          { return "opus" }
func (Opus) EncoderArgs() []string { return []string{"libopus"} }
