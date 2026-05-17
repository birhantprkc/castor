package transcode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"

	"github.com/stupside/castor/internal/app"
	"github.com/stupside/castor/internal/media"
)

// Transcode starts an ffmpeg process that reads from sourceURL and writes
// transcoded output to a pipe. Returns the readable end, a wait function
// for the process, and any startup error.
func Transcode(ctx context.Context, cfg app.TranscodeConfig, outputFormat string, sourceURL *url.URL, headers map[string]string) (io.ReadCloser, func() error, error) {
	args := []string{
		"-rw_timeout", strconv.FormatInt(cfg.RWTimeout.Microseconds(), 10),
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-readrate", strconv.Itoa(cfg.ReadRate),
		"-readrate_initial_burst", strconv.Itoa(cfg.ReadRateBurst),
		"-fflags", "+genpts+discardcorrupt",
	}

	if h := media.FormatHTTPHeaders(headers); h != "" {
		args = append(args, "-headers", h)
	}

	args = append(args, media.HLSInputArgs...)
	args = append(args,
		"-i", sourceURL.String(),
		"-c:v", cfg.VideoCodec,
		"-c:a", cfg.AudioCodec,
	)

	if cfg.AudioCodec != "copy" {
		args = append(args,
			"-ar", strconv.Itoa(cfg.AudioSampleRate),
			"-b:a", cfg.AudioBitrate,
		)
	}

	// mpegts: resend PAT/PMT and zero the mux preroll so renderers can begin
	// decoding mid-stream; h264_mp4toannexb rewrites fMP4/CMAF NAL units that
	// Samsung renderers refuse to parse otherwise.
	if outputFormat == "mpegts" {
		args = append(args,
			"-mpegts_flags", "+resend_headers+initial_discontinuity",
			"-muxdelay", "0",
			"-muxpreload", "0",
		)
		if cfg.VideoCodec == "copy" {
			args = append(args, "-bsf:v", "h264_mp4toannexb")
		}
	}

	args = append(args, "-f", outputFormat, "pipe:1")

	cmd := exec.CommandContext(ctx, cfg.FFmpegPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	slog.DebugContext(ctx, "ffmpeg started", "source", sourceURL.String(), "video_codec", cfg.VideoCodec, "audio_codec", cfg.AudioCodec, "format", outputFormat)

	go drainStderr(ctx, stderr)

	return stdout, cmd.Wait, nil
}

// drainStderr reads stderr line-by-line and logs each line at debug level.
func drainStderr(ctx context.Context, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.DebugContext(ctx, "ffmpeg", "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.WarnContext(ctx, "ffmpeg stderr scanner error", "error", err)
	}
}
