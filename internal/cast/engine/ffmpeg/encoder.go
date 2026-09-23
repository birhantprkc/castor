package ffmpeg

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/media"
)

const vaapiRenderNode = "/dev/dri/renderD128"

// Shared encoder flags; pix_fmt invariant on CPU encoders; VA-API uses GPU format.
var (
	// veryfast keeps the software encoders ahead of realtime in the live pipeline.
	softwareFlags     = []string{"-preset", "veryfast", "-pix_fmt", "yuv420p"}
	videotoolboxFlags = []string{"-pix_fmt", "yuv420p", "-g", "600"}
	vaapiFilters      = []string{"format=nv12", "hwupload"}
)

// Platform-agnostic; SelectEncoder tests which backends work here.
var (
	libx264 = plan.Encoder{Name: "libx264", Codec: media.CodecH264, Flags: softwareFlags}
	libx265 = plan.Encoder{Name: "libx265", Codec: media.CodecHEVC, Flags: slices.Concat(softwareFlags, []string{"-x265-params", "log-level=error"})}

	h264VideoToolbox = plan.Encoder{Name: "h264_videotoolbox", Codec: media.CodecH264, Hardware: true, Flags: videotoolboxFlags}
	hevcVideoToolbox = plan.Encoder{Name: "hevc_videotoolbox", Codec: media.CodecHEVC, Hardware: true, Flags: videotoolboxFlags}
	h264NVENC        = plan.Encoder{Name: "h264_nvenc", Codec: media.CodecH264, Hardware: true, Flags: []string{"-preset", "p4", "-pix_fmt", "yuv420p"}}
	hevcNVENC        = plan.Encoder{Name: "hevc_nvenc", Codec: media.CodecHEVC, Hardware: true, Flags: []string{"-preset", "p4", "-pix_fmt", "yuv420p"}}
	h264QSV          = plan.Encoder{Name: "h264_qsv", Codec: media.CodecH264, Hardware: true, Flags: []string{"-preset", "veryfast", "-pix_fmt", "nv12"}}
	hevcQSV          = plan.Encoder{Name: "hevc_qsv", Codec: media.CodecHEVC, Hardware: true, Flags: []string{"-preset", "veryfast", "-pix_fmt", "nv12"}}
	h264AMF          = plan.Encoder{Name: "h264_amf", Codec: media.CodecH264, Hardware: true, Flags: []string{"-quality", "speed", "-pix_fmt", "nv12"}}
	hevcAMF          = plan.Encoder{Name: "hevc_amf", Codec: media.CodecHEVC, Hardware: true, Flags: []string{"-quality", "speed", "-pix_fmt", "nv12"}}

	h264VAAPI = vaapiEncoder("h264_vaapi", media.CodecH264, vaapiRenderNode)
	hevcVAAPI = vaapiEncoder("hevc_vaapi", media.CodecHEVC, vaapiRenderNode)
)

// All encoders, grouped by codec; hardware before software.
var registry = []plan.Encoder{
	h264VideoToolbox, h264NVENC, h264QSV, h264VAAPI, h264AMF, libx264,
	hevcVideoToolbox, hevcNVENC, hevcQSV, hevcVAAPI, hevcAMF, libx265,
}

// This host's encoder strategies; discovers VA-API nodes without exposing paths.
func encoderCandidates() []plan.Encoder {
	out := slices.Clone(registry)
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	for _, node := range nodes {
		if node == vaapiRenderNode {
			continue
		}
		out = append(out, vaapiEncoder("h264_vaapi", media.CodecH264, node), vaapiEncoder("hevc_vaapi", media.CodecHEVC, node))
	}
	return out
}

func vaapiEncoder(name string, codec media.Codec, node string) plan.Encoder {
	alias := "va"
	return plan.Encoder{
		Name: name, Codec: codec, Hardware: true,
		InitArgs: []string{"-init_hw_device", "vaapi=" + alias + ":" + node, "-filter_hw_device", alias},
		Filters:  vaapiFilters,
	}
}

// Encoders binds adapter to ffmpeg binary path; part of port config.
func Encoders(ffmpegPath string) plan.Encoders {
	return func(ctx context.Context, codec media.Codec) (plan.Encoder, bool) {
		return SelectEncoder(ctx, ffmpegPath, codec)
	}
}

// SelectEncoder picks best working encoder; hardware first, then software; cached.
func SelectEncoder(ctx context.Context, ffmpegPath string, codec media.Codec) (enc plan.Encoder, ok bool) {
	candidates := encoderCandidates()
	for _, e := range candidates {
		if e.Codec == codec && e.Hardware && available(ctx, ffmpegPath, e) {
			slog.InfoContext(ctx, "hardware encoder selected", "encoder", e.Name, "codec", string(codec))
			return e, true
		}
	}
	for _, e := range candidates {
		if e.Codec == codec && !e.Hardware && available(ctx, ffmpegPath, e) {
			slog.InfoContext(ctx, "software encoder selected", "encoder", e.Name, "codec", string(codec))
			return e, true
		}
	}
	return plan.Encoder{}, false
}

// Caches encoder test-encode result; working GPU proven once.
var (
	availMu    sync.Mutex
	availCache = map[string]bool{}
)

func available(ctx context.Context, ffmpegPath string, e plan.Encoder) bool {
	availMu.Lock()
	defer availMu.Unlock()
	key := strings.Join(slices.Concat([]string{ffmpegPath, e.Name}, e.InitArgs, e.Filters, e.Flags), "\x00")
	if v, cached := availCache[key]; cached {
		return v
	}
	ok := testEncode(ctx, ffmpegPath, e)
	// Don't cache on context cancel; would disable working hardware.
	if ctx.Err() == nil {
		availCache[key] = ok
	}
	if !ok {
		slog.InfoContext(ctx, "encoder unavailable", "encoder", e.Name, "hardware", e.Hardware)
	}
	return ok
}

// testEncode: one-frame encode test; reuses device setup/filters for accuracy.
func testEncode(ctx context.Context, ffmpegPath string, e plan.Encoder) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append([]string{"-hide_banner"}, e.InitArgs...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=256x144:rate=25:duration=0.1")
	if len(e.Filters) > 0 {
		args = append(args, "-vf", strings.Join(e.Filters, ","))
	}
	args = append(args, "-c:v", e.Name)
	args = append(args, e.Flags...)
	args = append(args, "-f", "null", "-")
	return exec.CommandContext(ctx, ffmpegPath, args...).Run() == nil
}
