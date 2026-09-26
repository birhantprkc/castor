package codec

type AC3 struct{}

func (AC3) Name() string          { return "ac3" }
func (AC3) EncoderArgs() []string { return []string{"ac3"} }
