package codec

type MP3 struct{}

func (MP3) Name() string          { return "mp3" }
func (MP3) EncoderArgs() []string { return []string{"libmp3lame"} }
