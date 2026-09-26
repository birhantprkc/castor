package codec

// AV1 is SVT-AV1 at its fastest preset, the only AV1 encoder quick enough for fixtures.
type AV1 struct{}

func (AV1) Name() string          { return "av1" }
func (AV1) EncoderArgs() []string { return []string{"libsvtav1", "-preset", "12"} }
