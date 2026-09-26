package execute

import (
	"context"
	"path/filepath"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/cast/transcode"
)

func (c *cast) read(ctx context.Context) error {
	source, err := transcode.NewProgramSource(c.program, c.attempt.Read, c.cfg.Binary)
	if err != nil {
		return err
	}
	facts := measure(ctx, "the source this cast buffers", c.cfg.Probes.Source(c.program, source.ProbeInputs()))
	program := aligned(c.program, facts.Probe.InputStarts)
	if source, err = transcode.NewProgramSource(program, c.attempt.Read, c.cfg.Binary); err != nil {
		return err
	}

	if c.cfg.Subtitles != nil && (!facts.Measured || facts.Probe.AudioCodec != "") {
		c.burn = c.cfg.Subtitles(ctx, c.workDir)
	}

	floor, err := plan.Floor(ctx, plan.Inputs{
		Probe:     facts.Probe,
		Into:      transcode.SpoolFormat,
		Decode:    c.attempt.Decode,
		MaxHeight: c.cfg.MaxHeight,
		Spliced:   program.Seamed(),
		Encoders:  c.cfg.Encoders,
	})
	if err != nil {
		return err
	}
	logRefusals(ctx, floor)

	reader, err := startPull(ctx, pullSpec{
		ffmpegPath: c.cfg.FFmpegPath,
		program:    program,
		policy:     c.attempt.Read,
		source:     source,
		probe:      facts.Probe,
		spoolPath:  filepath.Join(c.workDir, "spool"+transcode.SpoolFormat.Extension),
		floor:      floor,
		pcmRate:    c.pcmRate(),
	})
	if err != nil {
		return err
	}
	c.reader, c.spool = reader, reader.spool

	c.evidence.Reached = attempt.PhaseReading
	return nil
}

func (c *cast) transcribe(ctx context.Context) error {
	if c.burn == nil {
		return nil
	}
	c.group.Go(func() error {
		c.burn.Run(ctx, c.reader.pcm)
		return nil
	})
	return nil
}

// readErr is the read's own terminal error, and only where the read has terminated (see pull.Err).
func (c *cast) readErr() error { return c.reader.Err() }

// pcmRate is the rate the transcription listens at, zero where this cast runs none.
func (c *cast) pcmRate() int {
	if c.burn == nil {
		return 0
	}
	return c.burn.SampleRate()
}
