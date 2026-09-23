package source

import (
	"strconv"
	"strings"

	"github.com/stupside/castor/internal/media"
)

var videoCodecEntries = map[string]media.Codec{
	"avc1": media.CodecH264,
	"avc3": media.CodecH264,
	"hvc1": media.CodecHEVC,
	"hev1": media.CodecHEVC,
	"av01": media.CodecAV1,
	"vp08": media.CodecVP8,
	"vp09": media.CodecVP9,
	"mp4v": media.CodecMPEG4,
	"dvh1": "",
	"dvhe": "",
}

var declaredAudioEntries = map[string]media.Codec{
	"mp4a.40.2":  media.CodecAAC,
	"mp4a.40.02": media.CodecAAC,
	"mp4a.40.5":  media.CodecAAC,
	"mp4a.40.05": media.CodecAAC,
	"mp4a.40.29": media.CodecAAC,
	"mp4a.40.34": media.CodecMP3,
	"mp4a.69":    media.CodecMP3,
	"mp4a.6b":    media.CodecMP3,
	"ac-3":       media.CodecAC3,
	"ec-3":       media.CodecEAC3,
}

var h264Profiles = map[uint64]media.Profile{
	0x42: media.ProfileBaseline,
	0x4d: media.ProfileMain,
	0x58: media.ProfileExtended,
	0x64: media.ProfileHigh,
}

// DeclaresVideo reports that an RFC 6381 codecs list names a video sample entry.
func DeclaresVideo(codecs string) bool {
	for entry := range strings.SplitSeq(codecs, ",") {
		if _, video := videoCodecEntries[sampleEntryName(strings.ToLower(strings.TrimSpace(entry)))]; video {
			return true
		}
	}
	return false
}

func DeclaredEnvelope(codecs string, height int) *media.ProbeInfo {
	if strings.TrimSpace(codecs) == "" {
		return nil
	}
	envelope := media.ProbeInfo{VideoHeight: height}
	video, audio := 0, 0
	for entry := range strings.SplitSeq(codecs, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if _, carriesPicture := videoCodecEntries[sampleEntryName(entry)]; carriesPicture {
			codec, profile, ok := declaredVideo(entry)
			if !ok {
				return nil
			}
			envelope.VideoCodec, envelope.VideoProfile, envelope.VideoBitDepth = codec, profile, 8
			video++
			continue
		}
		codec, named := declaredAudioEntries[entry]
		if !named {
			return nil
		}
		envelope.AudioCodec = codec
		audio++
	}
	if video != 1 || audio > 1 {
		return nil
	}
	return &envelope
}

func declaredVideo(entry string) (media.Codec, media.Profile, bool) {
	codec := videoCodecEntries[sampleEntryName(entry)]
	_, params, stated := strings.Cut(entry, ".")
	if !stated {
		return "", "", false
	}
	switch codec {
	case media.CodecH264:
		if len(params) != 6 {
			return "", "", false
		}
		value, err := strconv.ParseUint(params, 16, 32)
		if err != nil {
			return "", "", false
		}
		profile, named := h264Profiles[value>>16]
		if !named {
			return "", "", false
		}
		if profile == media.ProfileBaseline && value&0x004000 != 0 {
			profile = media.ProfileConstrainedBaseline
		}
		return codec, profile, true
	case media.CodecHEVC:
		if profileIDC, _, _ := strings.Cut(params, "."); profileIDC != "1" {
			return "", "", false
		}
		return codec, media.ProfileMain, true
	}
	return "", "", false
}

func sampleEntryName(entry string) string {
	name, _, _ := strings.Cut(entry, ".")
	return name
}
