package probe

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

const probeEntries = "format=format_name,bit_rate,duration:" +
	"stream=codec_type,codec_name,profile,width,height,pix_fmt,color_transfer,channels:" +
	"stream_disposition=attached_pic"

func decodeProbeTracks(out []byte, videoIndex, audioIndex int) (media.ProbeInfo, error) {
	var result struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			CodecName     string `json:"codec_name"`
			Profile       string `json:"profile"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			PixFmt        string `json:"pix_fmt"`
			ColorTransfer string `json:"color_transfer"`
			Channels      int    `json:"channels"`
			Disposition   struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			BitRate    string `json:"bit_rate"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return media.ProbeInfo{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	if result.Format.FormatName == "" {
		return media.ProbeInfo{}, fmt.Errorf("ffprobe returned no format name")
	}

	info := media.ProbeInfo{ContentType: formatToContentType(result.Format.FormatName)}
	// Non-numeric rate left at zero; no decision turns on it alone.
	info.BitRate, _ = strconv.ParseInt(result.Format.BitRate, 10, 64)
	if secs, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil && secs > 0 {
		info.Duration = time.Duration(secs * float64(time.Second))
	}

	videoSeen, audioSeen := 0, 0
	for _, s := range result.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition.AttachedPic != 0 {
				continue
			}
			info.VideoHeights = append(info.VideoHeights, s.Height)
			if videoSeen == videoIndex {
				// Dimensionless selected stream occupies real map index but establishes no picture envelope.
				if s.Width > 0 && s.Height > 0 {
					info.VideoCodec = media.Codec(s.CodecName)
					info.VideoProfile = media.Profile(s.Profile)
					info.VideoHeight = s.Height
					info.VideoBitDepth = pixFmtBitDepth(s.PixFmt)
					info.VideoHDR = isHDRTransfer(s.ColorTransfer)
				}
			}
			videoSeen++
		case "audio":
			if audioSeen == audioIndex {
				info.AudioCodec = media.Codec(s.CodecName)
				info.AudioChannels = s.Channels
			}
			audioSeen++
		}
	}
	return info, nil
}

// pixFmtBitDepth: 8-bit pix_fmts (yuv420p, nv12) carry no depth marker; 10/12-bit ones do.
func pixFmtBitDepth(pixFmt string) int {
	switch {
	case pixFmt == "":
		return 0
	case strings.Contains(pixFmt, "12"):
		return 12
	case strings.Contains(pixFmt, "10"):
		return 10
	default:
		return 8
	}
}

func isHDRTransfer(transfer string) bool {
	switch transfer {
	case "smpte2084", "arib-std-b67":
		return true
	default:
		return false
	}
}

// formatToContentType maps an ffprobe format_name to a content type.
func formatToContentType(format string) string {
	for f := range strings.SplitSeq(format, ",") {
		switch strings.TrimSpace(f) {
		case "hls", "applehttp":
			return media.HLS
		case "dash":
			return media.DASH
		// "mp4" is checked but "mov" deliberately is not: ffprobe reports the same joined list for both.
		case "mp4":
			return media.MP4
		case "matroska":
			return media.MKV
		case "webm":
			return media.WebM
		case "avi":
			return media.AVI
		case "mpegts":
			return media.MPEGTS
		case "flv":
			return media.FLV
		}
	}
	return ""
}
