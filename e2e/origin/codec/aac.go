package codec

type AAC struct{}

func (AAC) Name() string          { return "aac" }
func (AAC) EncoderArgs() []string { return []string{"aac"} }
