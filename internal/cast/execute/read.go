package execute

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/subtitle"
)

// buffered is a read into castor's own spool, and the transcription it feeds (nil where it feeds none).
type buffered struct {
	reader *pull
	burn   Burn
}

// read buffers program under its own context, so releasing it stops the read and its transcription before the work dir goes.
func (s *session) read(work string, program media.Program) (*buffered, error) {
	ctx, stop := context.WithCancel(s.ctx)
	source, err := transcode.NewProgramSource(program, s.attempt.Fetch, s.cfg.Binary)
	if err != nil {
		stop()
		return nil, err
	}
	facts := measure(ctx, "the source this cast buffers", s.cfg.Probes.Source(program, source.ProbeInputs()))
	program = aligned(program, facts.probe.InputStarts)
	if source, err = transcode.NewProgramSource(program, s.attempt.Fetch, s.cfg.Binary); err != nil {
		stop()
		return nil, err
	}

	burn := s.transcription(ctx, work, facts, program)
	floor, err := plan.Floor(ctx, plan.Inputs{
		Probe:     facts.probe,
		Into:      transcode.SpoolFormat,
		Decode:    s.attempt.Decode,
		MaxHeight: s.cfg.MaxHeight,
		Spliced:   program.Seamed(),
		Encoders:  s.cfg.Encoders,
	})
	if err != nil {
		stop()
		return nil, err
	}
	logRefusals(ctx, floor)

	var pcmRate int
	if burn != nil {
		pcmRate = subtitle.SampleRate
	}
	reader, err := startPull(ctx, pullSpec{
		ffmpegPath: s.cfg.FFmpegPath,
		program:    program,
		policy:     s.attempt.Fetch,
		source:     source,
		probe:      facts.probe,
		spoolPath:  filepath.Join(work, "spool"+transcode.SpoolFormat.Extension),
		floor:      floor,
		pcmRate:    pcmRate,
	})
	if err != nil {
		stop()
		return nil, err
	}

	transcribed := make(chan struct{})
	go func() {
		defer close(transcribed)
		if burn != nil {
			burn.Run(ctx, reader.pcm)
		}
	}()
	s.releases.push(func() error {
		stop()
		<-reader.Done()
		<-transcribed
		return nil
	})
	return &buffered{reader: reader, burn: burn}, nil
}

// transcription is the burn-in this read feeds, nil where subtitles are off or nothing shows the source has sound.
func (s *session) transcription(ctx context.Context, work string, facts facts, program media.Program) Burn {
	if s.cfg.Subtitles == nil {
		return nil
	}
	if !facts.sounds(program) {
		slog.InfoContext(ctx, "no subtitles for this cast: nothing shows the source carries sound")
		return nil
	}
	return s.cfg.Subtitles(ctx, work)
}
