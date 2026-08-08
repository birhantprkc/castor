// Package e2e answers one question about castor, end to end: given an input
// stream, does it produce a stream that plays.
//
// Everything on both sides is real. A real ffmpeg produces a genuinely live HLS
// stream into a real HTTP origin; castor's own probe measures it, castor's own
// resolvers decide what to copy and what to re-encode, castor's own argument
// builder turns that into a command line, and a real ffmpeg runs it. What comes
// out is read back with a real ffprobe and asserted on by packet count, because a
// container will happily declare a track it never wrote a packet into.
//
// Nothing here knows what a Chromecast is. Discovery, device protocols and the
// decision to relay at all are covered by their own suites; the only part of them
// that changes the bytes is which container castor was asked to write and which
// codecs the far end decodes, and both are plain data a cell states directly. So
// there is no fake television, no SOAP, and no subprocess.
//
// Live is the point. The unit suites cover fixed-length fixtures, which never
// exercise a playlist that rolls while it is being read, a source with no
// duration, or production that has to be stopped rather than waited out.
//
// The matrix crosses input shape with output container because handling that is
// right for one container is destructive for another: MPEG-TS repeats decoder
// configuration in band ahead of every frame, while the MP4 family declares it
// once up front, and a bitstream framed for one is rejected or silently gutted by
// the other. It also crosses the read-once spool, which re-frames everything
// passing through it, so an input whose audio arrived out of band comes back in
// band and needs the opposite treatment.
//
// Run them with:
//
//	eval "$(make env)" && go test ./e2e/
//
// They need ffmpeg and ffprobe on PATH and are slower than the unit suites, since
// every cell reads a stream in real time. Use -short to skip them.
package e2e
