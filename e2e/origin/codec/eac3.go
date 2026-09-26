package codec

type EAC3 struct{}

func (EAC3) Name() string          { return "eac3" }
func (EAC3) EncoderArgs() []string { return []string{"eac3"} }
