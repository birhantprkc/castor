package origin

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// DefaultRate is the synthetic source's frame rate when no quirk sets one.
const DefaultRate = "15"

// FPS is the source's frame rate as a number, reading ffmpeg's rational form too.
func (s Stream) FPS() float64 {
	rate := cmp.Or(s.Rate, DefaultRate)
	if num, den, ok := strings.Cut(rate, "/"); ok {
		n, _ := strconv.ParseFloat(num, 64)
		d, _ := strconv.ParseFloat(den, 64)
		return n / d
	}
	f, _ := strconv.ParseFloat(rate, 64)
	return f
}

// encodeArgs is the encoder half: synthetic tracks, one keyframe a second so every one-second segment opens on one.
// Each rung is its own video stream; muxed audio is mapped once per rung, since it travels inside every rendition.
func (s Stream) encodeArgs() []string {
	inputs := slices.Concat([]string{"-hide_banner", "-loglevel", "error", "-y"}, s.VideoIn,
		[]string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%dx%d:rate=%s", width(s.Height()), s.Height(), cmp.Or(s.Rate, DefaultRate))})
	// The transfer tags frames before anything else, so every rendition carries it.
	chain := slices.DeleteFunc(slices.Concat([]string{s.Transfer.Filter()}, s.Filters, []string{fmt.Sprintf("split=%d", len(s.Heights))}),
		func(f string) bool { return f == "" })
	splits, scales := make([]string, len(s.Heights)), make([]string, len(s.Heights))
	mapping := []string{"-t", strconv.Itoa(s.Seconds)}
	for i, h := range s.Heights {
		splits[i] = fmt.Sprintf("[s%d]", i)
		scales[i] = fmt.Sprintf("[s%d]scale=-2:%d[r%d]", i, h, i)
		mapping = append(mapping, "-map", fmt.Sprintf("[r%d]", i))
	}
	filter := fmt.Sprintf("[0:v]%s%s;%s", strings.Join(chain, ","), strings.Join(splits, ""), strings.Join(scales, ";"))
	mapping = append([]string{"-filter_complex", filter}, mapping...)
	gop := strconv.Itoa(int(math.Round(s.FPS())))
	codecs := slices.Concat([]string{"-c:v"}, s.Video.EncoderArgs(),
		[]string{"-pix_fmt", pixelFormats[s.Depth], "-g", gop}, s.VideoOut)
	if s.Audio != nil {
		inputs = slices.Concat(inputs, s.AudioIn, []string{"-f", "lavfi", "-i", "sine=frequency=440"})
		copies := map[Carriage]int{Muxed: len(s.Heights), Separate: 1}[s.Carriage]
		for range copies {
			mapping = append(mapping, "-map", "1:a")
		}
		codecs = slices.Concat(codecs, []string{"-c:a"}, s.Audio.EncoderArgs(), []string{"-ac", strconv.Itoa(s.Channels)}, s.AudioOut)
	}
	return slices.Concat(inputs, mapping, codecs, s.MuxOut)
}
