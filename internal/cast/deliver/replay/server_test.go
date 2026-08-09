package replay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/watch"
)

// This file is the first cover this package has ever had. It fronts every non-segmented
// cast castor makes, and the properties below are the ones a renderer's behaviour actually
// depends on: replay from byte 0 for every client, a reconnect served from where it stopped
// rather than from the beginning, a truthful account of whether anybody fetched, and a bound on
// how long one client may hold a goroutine that accounts for what it costs when it fires.

// TestEveryClientReplaysFromByteZero is the whole reason this server spools instead of
// broadcasting. A renderer probes with HEAD, then a short GET, then the real GET, and a live
// fan-out hands the probe the only copy of the stream head: the real GET then joins at an
// arbitrary byte offset with no container init and no keyframe, and decodes nothing.
func TestEveryClientReplaysFromByteZero(t *testing.T) {
	want := bytes.Repeat([]byte("castor"), 4096)
	srv := serve(t, bytes.NewReader(want))

	// The probe dance, in order, against one produced stream.
	if code := head(t, srv); code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", code)
	}
	for _, name := range []string{"the short probe GET", "the real GET"} {
		got := fetch(t, srv, len(want))
		if !bytes.Equal(got, want) {
			t.Errorf("%s received %d bytes, want the whole stream from byte 0 (%d bytes)", name, len(got), len(want))
		}
	}
}

// TestHandedCountsTheProgramTakenAndNotTheAsking is what makes "the renderer accepted Play and
// never came for the bytes" sayable. Every other fact this server tracks is satisfied by a cast
// nobody ever fetched (its idle grace starts running the moment it is created and its finished
// condition needs no client at all), which is how castor encoded an entire title and reported it
// as delivered.
//
// The figure is bytes and not requests, and that is the whole of what it is worth. A request is
// answered before a byte of it is written, and this server's clients come as a HEAD, then a
// short GET, then the real GET, so a count says "the renderer fetched" about a renderer that
// asked and got nothing.
func TestHandedCountsTheProgramTakenAndNotTheAsking(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 8192)
	srv := serve(t, bytes.NewReader(body))

	if handed, last := srv.Handed(); handed != 0 || !last.IsZero() {
		t.Fatalf("a stream nobody has taken reports %d bytes handed over at %v", handed, last)
	}

	// A HEAD takes no byte of the program. A renderer that probes the URL and never gets the
	// stream is the exact failure this reports, so crediting its probe would answer that it was
	// watching.
	if code := head(t, srv); code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", code)
	}
	if handed, last := srv.Handed(); handed != 0 || !last.IsZero() {
		t.Errorf("a HEAD probe was credited with %d bytes at %v", handed, last)
	}

	before := time.Now()
	fetch(t, srv, len(body))
	handed, last := srv.Handed()
	if handed != int64(len(body)) {
		t.Errorf("handed = %d after a client read the whole stream, want the %d bytes it took: a count of requests would report 1 here", handed, len(body))
	}
	if last.Before(before) {
		t.Errorf("last fetch = %v, which predates the GET at %v: the recency a quiet renderer is judged on never advanced", last, before)
	}
}

// TestARendererThatAsksAndTakesNothingIsNotFetching is the shape the verdict about a renderer
// could not see while this server counted requests, and it is the shape the field evidence has:
// a URL accepted, a request in the log, bytes_sent=0.
//
// Both halves of the answer are asserted, because either one alone hides it. A count credits the
// asking, and the recency clock restarted at the end of every connection whether or not a byte
// had moved, so a renderer probing on any cadence stayed permanently inside the window the
// verdict waits out. Nothing then names it: castor produces the whole title for a renderer that
// took none of it and exits 0.
func TestARendererThatAsksAndTakesNothingIsNotFetching(t *testing.T) {
	// A producer that yields nothing, so a client that comes for the stream is answered and
	// handed no byte of it. Wait is the barrier rather than a sleep: it returns only once this
	// client has been fully accounted for (the producer is done, the client reached the end of
	// what there was, and the connection count is back to zero), so the assertion below reads a
	// settled server.
	srv := serve(t, bytes.NewReader(nil))
	fetch(t, srv, 0)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(ctx); err != nil {
		t.Fatalf("Wait after the one client came and went = %v, want nil", err)
	}

	handed, last := srv.Handed()
	if handed != 0 {
		t.Errorf("a renderer that asked and was handed nothing is credited with %d bytes, which is the request being counted rather than the program", handed)
	}
	if !last.IsZero() {
		t.Errorf("a request that moved no byte set the recency clock to %v: a renderer probing on a cadence is then never quiet for long enough to be named, however little of the program it ever took", last)
	}
}

// TestAClientThatStopsReadingIsSevered pins the bound on one chunk write. Without it a
// renderer that stops reading parks this goroutine on a blocked socket forever, and the cast
// hangs outright rather than ending: Wait needs the client count to reach zero, and a
// goroutine wedged inside Write never decrements it.
func TestAClientThatStopsReadingIsSevered(t *testing.T) {
	// Far more than any socket buffer holds, so the server is certain to be blocked in a
	// write while the client is not reading.
	const size = 8 << 20
	srv := serveWith(t, bytes.NewReader(bytes.Repeat([]byte("y"), size)), 100*time.Millisecond)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read one byte, then stop, well past the deadline the server is holding itself to.
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second)

	// Whatever the client does next, it cannot be handed the rest of the stream: the server
	// gave up on it while it was not reading.
	n, _ := io.Copy(io.Discard, resp.Body)
	if n+1 >= size {
		t.Errorf("the client was handed all %d bytes after ignoring the socket for ten deadlines, so nothing bounded the write", size)
	}
}

// TestTheWriteDeadlineOutlastsEverySilenceCastorTolerates is the derivation, as an assertion.
// Severing here is expensive (a reconnect that does not ask to continue replays from byte 0, so
// the film starts over) and it is the ONLY answer a quiet renderer gets: no verdict convicts
// one, because a pause, a viewer who walked away and a crashed renderer are the same absence of
// anything being taken. So it must outlast the longest silence any judgement castor makes
// tolerates, which is watch.StallWindow, or a viewer who stands up loses their position sooner
// than castor tolerates silence from anybody else.
func TestTheWriteDeadlineOutlastsEverySilenceCastorTolerates(t *testing.T) {
	if DefaultWriteDeadline <= watch.StallWindow {
		t.Errorf("DefaultWriteDeadline = %s, at or inside the %s castor tolerates silence for elsewhere: a pause shorter than castor's own patience costs the viewer the cast",
			DefaultWriteDeadline, watch.StallWindow)
	}
}

// TestHandedIsTheMostAnyOneClientWasGiven is the figure a cast is judged on, while it runs
// (watch.Consumer) and once it is over (core.Undelivered), and the way it is counted is what
// makes that judgement honest.
//
// The MOST, and not the sum: every connection replays from byte 0, so a renderer's probe GET
// and its real GET overlap byte for byte, and adding them would report a renderer that took
// the head twice as having taken the film. Any ONE connection is what actually got through, so
// the longest of them is what the renderer received.
func TestHandedIsTheMostAnyOneClientWasGiven(t *testing.T) {
	// Far more than any socket buffer holds, so a client that stops reading really does leave
	// the server unable to hand over the rest.
	const size = 8 << 20
	srv := serveWith(t, bytes.NewReader(bytes.Repeat([]byte("w"), size)), 300*time.Millisecond)

	if got := handedTo(srv); got != 0 {
		t.Fatalf("a stream nobody has fetched reports %d bytes handed over", got)
	}

	stall(t, srv)
	partial := handedTo(srv)
	if partial <= 0 || partial >= size {
		t.Fatalf("a client that read one byte and stopped was handed %d of %d bytes, want a fraction of the stream", partial, size)
	}

	fetch(t, srv, size)
	if got := handedTo(srv); got != size {
		t.Errorf("after a client read the whole stream, the figure handed over = %d, want all %d bytes", got, size)
	}

	// A second client that gives up takes nothing away from what the first one received, and
	// adds nothing to it either.
	stall(t, srv)
	if got := handedTo(srv); got != size {
		t.Errorf("the figure handed over = %d after a later client gave up, want the %d bytes one client really took: the sum would credit a renderer with a head it was handed twice", got, size)
	}
}

// TestASeveredClientResumesWhereItStopped is what a pause costs where the delivery takes
// ranges, as an assertion. A viewer who pauses stops draining the socket, the blocked write is
// severed at the write deadline, and every connection replays from byte 0: without this the
// renderer's next GET restarts the program from the beginning, at whatever point the viewer had
// reached, with no error anywhere. The spool still holds every byte that renderer had, so the
// offset it asks for is servable.
//
// It is half the answer, and the delivery decides which half applies: see
// TestOnADeliveryThatTakesNoRangesAPausePastTheDeadlineCostsThePosition for the other one.
func TestASeveredClientResumesWhereItStopped(t *testing.T) {
	want := payload(8 << 20)
	srv := serveWith(t, bytes.NewReader(want), 300*time.Millisecond)

	// The pause: one byte taken, then nothing, until the server gives up on the connection.
	stall(t, srv)

	from := int64(len(want) / 2)
	resp := get(t, srv, fmt.Sprintf("bytes=%d-", from))
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("a client asking to continue at byte %d got %d, want 206: a 200 is the whole film again from the beginning", from, resp.StatusCode)
	}
	stated := fmt.Sprintf("bytes %d-%d/%d", from, len(want)-1, len(want))
	if got := resp.Header.Get("Content-Range"); got != stated {
		t.Errorf("Content-Range = %q, want %q", got, stated)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want[from:]) {
		t.Errorf("the resumed client was handed %d bytes, want the %d from byte %d onwards", len(got), len(want)-int(from), from)
	}
}

// TestOnADeliveryThatTakesNoRangesAPausePastTheDeadlineCostsThePosition is the other half of
// what a pause costs, and it is the half that decides most casts: the resume above is live only
// where the delivery's own responses take ranges, and the family castor serves a renderer that
// cannot fetch for itself declares Accept-Ranges: none.
//
// Here the two mechanisms meet, which is why this is asserted over a real severance rather than
// over a bare range request. The write deadline destroys the viewer's position (nothing else can
// bound a human pause), the reconnect that asks to continue is refused because castor may not
// contradict its own promise to the firmware, and the film starts over from byte 0. What must not
// happen is that happening quietly: the severance states that no resume was available and the
// refusal names the declaration, because these two lines are the whole account of a film that
// restarted itself.
func TestOnADeliveryThatTakesNoRangesAPausePastTheDeadlineCostsThePosition(t *testing.T) {
	log := logged(t)
	want := payload(8 << 20)
	srv := serveDeclining(t, want)

	// The pause, ended by the write deadline exactly as it is on any other delivery.
	stall(t, srv)

	rec := log.await(t, slog.LevelWarn, "stopped draining")
	if got := attrs(rec)["resumable"]; got != "false" {
		t.Errorf("the severance reports resumable=%q on a delivery that answers no range: the log offers the reconnect a fix it does not have", got)
	}

	from := int64(len(want) / 2)
	resp := get(t, srv, fmt.Sprintf("bytes=%d-", from))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a client asking to continue at byte %d got %d, want the 200 its own declaration forces", from, resp.StatusCode)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the reconnect was served from somewhere other than byte 0, so a delivery that refuses ranges answered one anyway")
	}
	refusal := log.await(t, slog.LevelWarn, "replayed from the beginning")
	if reason := attrs(refusal)["reason"]; !strings.Contains(reason, "Accept-Ranges: none") {
		t.Errorf("the position was lost and the log reads %q, which does not name the declaration that cost it", reason)
	}
}

// TestHandedCreditsAResumedClientWithThePrefixItAlreadyHad guards the arithmetic a cast is judged
// on. Asking for byte N is the client stating it holds 0 to N-1, so a renderer that resumed at
// the half-way mark and watched to the end took the whole film. Counting only the resumed
// connection's own bytes reports half of it, and the completeness statement then convicts a
// renderer that did nothing wrong (see core.Undelivered).
func TestHandedCreditsAResumedClientWithThePrefixItAlreadyHad(t *testing.T) {
	want := payload(1 << 20)
	srv := serveWith(t, bytes.NewReader(want), 300*time.Millisecond)

	from := int64(len(want) / 2)
	resp := get(t, srv, fmt.Sprintf("bytes=%d-", from))
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if got := handedTo(srv); got != int64(len(want)) {
		t.Errorf("the figure handed over = %d after a client resumed at %d and read to the end, want the whole %d bytes it holds", got, from, len(want))
	}
}

// TestAResumeIsRefusedWhileNobodyCanSayWhereTheStreamEnds is the boundary of the fix, and it is
// where the resume STOPS: while the encoder is producing, every shape of range is replayed from
// byte 0.
//
// A 206 has to name a last byte and the length it is a stretch of, and while the producer runs the
// only candidates are numbers nobody knows yet or the bytes produced so far. Stating the second
// tells a client the film ends where the encoder happened to have reached, and it stops mid-title
// with no error: the restart is the lesser failure, so the client is replayed from byte 0 and told
// why in the log.
//
// A client naming its own last byte used to be answered here, on the argument that it invents no
// length of its own. That half is gone rather than fixed: no renderer castor serves has ever sent
// such a range (the family it serves for itself declares Accept-Ranges: none and its GETs carry no
// range at all), so it was arithmetic over a moving file written for nobody, and one shape of
// refusal is one thing to get right.
func TestAResumeIsRefusedWhileNobodyCanSayWhereTheStreamEnds(t *testing.T) {
	for _, asked := range []string{"bytes=%d-", "bytes=%d-65535"} {
		t.Run(asked, func(t *testing.T) {
			log := logged(t)
			head := payload(64 << 10)
			srv := serveProducing(t, head)

			resp := get(t, srv, fmt.Sprintf(asked, len(head)/2))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: a range answered over a stream with no stated end is a length castor invented", resp.StatusCode)
			}
			got := make([]byte, len(head))
			if _, err := io.ReadFull(resp.Body, got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, head) {
				t.Errorf("the refused client was served from somewhere other than byte 0, so it was handed media it cannot decode")
			}
			rec := log.await(t, slog.LevelWarn, "replayed from the beginning")
			if reason := attrs(rec)["reason"]; !strings.Contains(reason, "still running") {
				t.Errorf("the refusal reads %q, which does not say why the position could not be honoured", reason)
			}
		})
	}
}

// TestABoundedResumeEndsWhereItSaidItWould is the other half of the arithmetic over a finished
// file: a client that names its own last byte is served exactly that stretch, and the response
// STOPS there.
//
// The stopping is the part worth a test. A handler that kept reading past its own declared length
// parks on a tail that yields nothing until somebody appends more: the client is long gone, the
// server still counts it as connected, and the cast cannot end while it does.
func TestABoundedResumeEndsWhereItSaidItWould(t *testing.T) {
	log := logged(t)
	want := payload(1 << 20)
	srv := serve(t, bytes.NewReader(want))

	from, to := int64(len(want)/2), int64(len(want)-1024)
	resp := get(t, srv, fmt.Sprintf("bytes=%d-%d", from, to))
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	stated := fmt.Sprintf("bytes %d-%d/%d", from, to, len(want))
	if got := resp.Header.Get("Content-Range"); got != stated {
		t.Errorf("Content-Range = %q, want %q", got, stated)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want[from:to+1]) {
		t.Errorf("the client was handed %d bytes, want exactly the %d it asked for", len(got), to-from+1)
	}
	log.await(t, slog.LevelInfo, "stream range delivered")
}

// TestAResumePastTheEndIsAnsweredRatherThanHung: a client asking for a byte after the end of a
// finished stream is told where the end is. Serving it instead parks the response on a tail that
// will never yield, and a 206 promising bytes that do not exist hands the renderer a body
// shorter than the length it was given.
func TestAResumePastTheEndIsAnsweredRatherThanHung(t *testing.T) {
	want := payload(4096)
	srv := serveWith(t, bytes.NewReader(want), 300*time.Millisecond)

	resp := get(t, srv, fmt.Sprintf("bytes=%d-", len(want)))
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", resp.StatusCode)
	}
	stated := fmt.Sprintf("bytes */%d", len(want))
	if got := resp.Header.Get("Content-Range"); got != stated {
		t.Errorf("Content-Range = %q, want %q: a client refused a range learns nothing unless it is told the length", got, stated)
	}
}

// TestAResumeThatTakesTheLastByteEndsTheCast: a client whose stretch ends on the stream's own
// last byte watched the film to the end, so the delivery is over. Reading that only from a tail
// reaching EOF misses it, because a bounded response stops on its stated length without ever
// asking for another byte, and the cast then waits out the idle grace for a renderer that has
// nothing left to want.
func TestAResumeThatTakesTheLastByteEndsTheCast(t *testing.T) {
	want := payload(4096)
	srv := serveWith(t, bytes.NewReader(want), 300*time.Millisecond)

	resp := get(t, srv, fmt.Sprintf("bytes=%d-%d", len(want)/2, len(want)-1))
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(ctx); err != nil {
		t.Fatalf("Wait after a client took the stream's last byte = %v, want nil", err)
	}
}

// TestADeviceThatDeclaresNoRangesIsNeverHandedAPartialResponse. The protocol headers on these
// responses are the DEVICE's statement about what it may ask for, and one family declares
// Accept-Ranges: none. Answering a 206 over that header is castor contradicting its own promise
// and handing a firmware the one response shape it was told would not arrive, so the declaration
// wins and the refusal names it: the header is then the thing to change, not this server.
func TestADeviceThatDeclaresNoRangesIsNeverHandedAPartialResponse(t *testing.T) {
	log := logged(t)
	want := payload(4096)
	srv := serveDeclining(t, want)

	resp := get(t, srv, fmt.Sprintf("bytes=%d-", len(want)/2))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a delivery whose own headers refuse ranges", resp.StatusCode)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the client was not replayed from byte 0")
	}
	rec := log.await(t, slog.LevelWarn, "replayed from the beginning")
	if reason := attrs(rec)["reason"]; !strings.Contains(reason, "Accept-Ranges: none") {
		t.Errorf("the refusal reads %q, which does not name the declaration that caused it", reason)
	}
}

// TestARefusedProbeFromByteZeroCostsNothingAndSaysNothing keeps the alarm meaningful. A renderer
// that probes with a short bounded GET from byte 0 and is handed the whole stream from byte 0 has
// lost no position, and the GET line already records what it asked for: warning about those puts
// one line per probe next to the one refusal that really costs a viewer their place.
func TestARefusedProbeFromByteZeroCostsNothingAndSaysNothing(t *testing.T) {
	log := logged(t)
	want := payload(4096)
	srv := serveDeclining(t, want)

	resp := get(t, srv, "bytes=0-15")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, len(want))); err != nil {
		t.Fatal(err)
	}
	// The refusal is logged before the first byte is written, so a body read to the end has
	// outlived any line this request was going to produce.
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, rec := range log.list {
		if rec.Level == slog.LevelWarn && strings.Contains(rec.Message, "replayed from the beginning") {
			t.Errorf("a probe from byte 0 was reported as having lost its position: %q", rec.Message)
		}
	}
}

// serveDeclining serves a stream over a delivery whose device declares that it takes no ranges,
// which is the shape one renderer family really is served under (device.StreamHeaders).
func serveDeclining(t *testing.T, body []byte) *Server {
	t.Helper()
	srv, err := New(Config{
		LocalIP:       "127.0.0.1",
		ContentType:   "video/mp2t",
		Extension:     ".ts",
		Headers:       map[string]string{"Accept-Ranges": "none"},
		SpoolPath:     filepath.Join(t.TempDir(), "out.ts"),
		WriteDeadline: 300 * time.Millisecond,
	}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	<-srv.ProducerDone()
	return srv
}

// TestSeveringAClientAccountsForTheRestartItCauses. The write deadline is allowed to destroy a
// viewer's position (nothing else can bound a human pause) but it is not allowed to do it
// silently: a film that starts itself over with only "client disconnected" in the log, at the
// same INFO level as a probe GET closing its socket, is a mystery from the outside. So the
// numbers that account for the restart are stated where they are known.
func TestSeveringAClientAccountsForTheRestartItCauses(t *testing.T) {
	log := logged(t)
	srv := serveWith(t, bytes.NewReader(payload(8<<20)), 300*time.Millisecond)
	stall(t, srv)

	rec := log.await(t, slog.LevelWarn, "stopped draining")
	got := attrs(rec)
	for _, key := range []string{"stalled_for", "bytes_sent", "write_deadline", "resumable"} {
		if got[key] == "" {
			t.Errorf("the severance names no %s, so the restart it causes cannot be accounted for: %v", key, got)
		}
	}
	if got["stalled_for"] == "0s" {
		t.Errorf("the severance reports a client that had been taking nothing for 0s, which explains nothing: %v", got)
	}
	if got["bytes_sent"] == "0" {
		t.Errorf("the severance reports 0 bytes handed over for a client that took some: %v", got)
	}
	if got["resumable"] != "true" {
		t.Errorf("resumable = %q over a finished producer with no range policy against it, so the log denies the reconnect a fix it actually has", got["resumable"])
	}
}

// payload is a stream whose every byte offset is identifiable, so a client served from the wrong
// offset is caught rather than passing on a repeated byte.
func payload(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i % 251)
	}
	return out
}

// stall fetches one byte and then ignores the socket for several write deadlines, which is
// what a renderer that went away looks like to this server.
func stall(t *testing.T, srv *Server) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
}

// TestCloseJoinsTheGoroutineSpoolingTheProducer is the guarantee the caller's next two steps rest
// on, and it was missing: this server reads the producer's pipe from a goroutine of its own, and
// nothing waited for it. The caller reaps the producer immediately after Close (os/exec closes that
// pipe inside Wait, underneath a copy still reading it) and removes the work directory right after
// that (deleting the file the copy writes into). Both were harmless by accident of timing and of
// POSIX tolerating writes to an unlinked file, while the opener's own doc claimed nothing was left
// writing.
//
// The other half of the contract is the reason it is safe to block here: Close is called by a
// caller that has already stopped the producer, so the copy is at EOF or about to be.
func TestCloseJoinsTheGoroutineSpoolingTheProducer(t *testing.T) {
	pr, pw := io.Pipe()
	srv, err := New(Config{
		LocalIP:     "127.0.0.1",
		ContentType: "video/mp2t",
		Extension:   ".ts",
		SpoolPath:   filepath.Join(t.TempDir(), "out.ts"),
	}, pr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(payload(4096)); err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- srv.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while the producer was still writing into the spool: the caller reaps that producer next and removes the directory it writes into", err)
	case <-time.After(200 * time.Millisecond):
	}

	// What the caller does before Close in production: it stops the producer, so the copy reaches
	// the end of its input.
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the producer ended")
	}
}

// TestWaitEndsWhenAClientHasReadTheStreamToEOF is the ordinary end of a cast: the producer
// finished and a client consumed everything. The producer finishing is explicitly not
// enough, because it runs ahead of playback.
func TestWaitEndsWhenAClientHasReadTheStreamToEOF(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 4096)
	srv := serve(t, bytes.NewReader(body))
	fetch(t, srv, len(body))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(ctx); err != nil {
		t.Fatalf("Wait after a client read the stream to EOF = %v, want nil", err)
	}
}

func serve(t *testing.T, producer io.Reader) *Server {
	t.Helper()
	return serveWith(t, producer, 0)
}

func serveWith(t *testing.T, producer io.Reader, writeDeadline time.Duration) *Server {
	t.Helper()
	srv, err := New(Config{
		LocalIP:       "127.0.0.1",
		ContentType:   "video/mp4",
		Extension:     ".mp4",
		SpoolPath:     filepath.Join(t.TempDir(), "out.mp4"),
		WriteDeadline: writeDeadline,
	}, producer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	// The stream is fully produced before any assertion, so a short read is the server's
	// answer and never a race with the producer.
	select {
	case <-srv.ProducerDone():
	case <-time.After(5 * time.Second):
		t.Fatal("the producer never finished spooling")
	}
	return srv
}

// serveProducing serves a stream whose producer has NOT finished, with head already spooled. It
// is the state a cast spends its first half in (the encoder runs ahead of playback), so it is
// the state most viewers pause in, and it is the state where nothing can state where the stream
// ends.
func serveProducing(t *testing.T, head []byte) *Server {
	t.Helper()
	pr, pw := io.Pipe()
	srv, err := New(Config{
		LocalIP:       "127.0.0.1",
		ContentType:   "video/mp4",
		Extension:     ".mp4",
		SpoolPath:     filepath.Join(t.TempDir(), "out.mp4"),
		WriteDeadline: 300 * time.Millisecond,
	}, pr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pw.Close()
		_ = srv.Close()
	})
	if _, err := pw.Write(head); err != nil {
		t.Fatal(err)
	}
	// The write returns once the copy has taken the bytes, which is not yet the spool having
	// them, so every assertion below is against a stream this much of has actually landed.
	for range 500 {
		if landed, _ := srv.Spooled(); landed >= int64(len(head)) {
			return srv
		}
		time.Sleep(10 * time.Millisecond)
	}
	landed, _ := srv.Spooled()
	t.Fatalf("only %d of %d bytes reached the spool", landed, len(head))
	return nil
}

// get fetches the stream, optionally asking to continue from an offset.
func get(t *testing.T, srv *Server, byteRange string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// logged captures what this server says for the duration of one test. A severance and a refused
// resume both destroy a viewer's position, and the log line is the whole of their attribution, so
// it is asserted on rather than trusted.
func logged(t *testing.T) *records {
	t.Helper()
	previous := slog.Default()
	kept := &records{}
	slog.SetDefault(slog.New(kept))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return kept
}

type records struct {
	mu   sync.Mutex
	list []slog.Record
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }
func (r *records) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *records) WithGroup(string) slog.Handler            { return r }

func (r *records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, rec.Clone())
	return nil
}

// await waits for a line, because the server logs on the goroutine serving the connection and a
// test that read the slice once would be racing it.
func (r *records) await(t *testing.T, level slog.Level, phrase string) slog.Record {
	t.Helper()
	for range 500 {
		r.mu.Lock()
		for _, rec := range r.list {
			if rec.Level == level && strings.Contains(rec.Message, phrase) {
				r.mu.Unlock()
				return rec
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing was logged at %s containing %q", level, phrase)
	return slog.Record{}
}

func attrs(rec slog.Record) map[string]string {
	out := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

// handedTo is how much of the program went out, for the assertions that are about the count and
// not about the clock beside it.
func handedTo(srv *Server) int64 {
	handed, _ := srv.Handed()
	return handed
}

func head(t *testing.T, srv *Server) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func fetch(t *testing.T, srv *Server, want int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make([]byte, want)
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatalf("reading the served stream: %v", err)
	}
	return got
}
