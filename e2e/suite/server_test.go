package suite

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/e2e/settings"
)

const (
	// readyWithin is how long a server process is given to log where it serves and answer its health check there.
	readyWithin = 30 * time.Second
	// stopGrace is how long an interrupted server is given to let go of its devices before its process group is killed.
	stopGrace = 15 * time.Second
)

// output is what a server process writes, while it writes it.
type output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *output) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Clone(o.buf.Bytes())
}

// server is a server binary running until the test ends.
type server struct {
	name   string
	out    output
	exited chan struct{}
	// err is how the process ended, set before exited closes.
	err error
}

// start runs bin under doc, stops it when the test ends, and logs its output if the test failed.
func start(t *testing.T, carrier settings.Carrier, doc map[string]any, bin binary) *server {
	t.Helper()
	launch, err := carrier.Carry(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{name: filepath.Base(string(bin)), exited: make(chan struct{})}
	// Not the test's context: a server is interrupted first, at cleanup, so its casts let go of their devices.
	cmd := bin.command(context.Background(), launch, nil, &s.out)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		s.err = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("%s's output:\n%s", s.name, s.out.bytes())
		}
	})
	t.Cleanup(func() {
		// Interrupted as a user would stop it, so its casts let go of their devices.
		if cmd.Process.Signal(os.Interrupt) == nil {
			select {
			case <-s.exited:
			case <-time.After(stopGrace):
			}
		}
		killGroup(cmd)
		<-s.exited
	})
	return s
}

// ready waits for s to log where it serves and to answer its health check there with token, and is that base URL.
func (s *server) ready(t *testing.T, token string) string {
	t.Helper()
	deadline := time.After(readyWithin)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var base string
	why := errors.New("it logged no serving address")
	for {
		if base == "" {
			if addr, found := served(s.out.bytes()); found {
				if base, why = baseURL(addr); why != nil {
					t.Fatalf("%s logged an address it serves at that is not one: %v", s.name, why)
				}
			}
		}
		if base != "" {
			if why = healthy(t, base, token); why == nil {
				return base
			}
		}
		select {
		case <-s.exited:
			t.Fatalf("%s exited before it was ready: %v", s.name, s.err)
		case <-deadline:
			t.Fatalf("%s was not ready within %v: %v", s.name, readyWithin, why)
		case <-tick.C:
		}
	}
}

// served is the address on the first complete line logging that castor is serving.
func served(logs []byte) (string, bool) {
	for line := range strings.Lines(string(logs)) {
		if !strings.HasSuffix(line, "\n") || !strings.Contains(line, "serving") {
			continue
		}
		for field := range strings.FieldsSeq(line) {
			if addr, ok := strings.CutPrefix(field, "address="); ok {
				return strings.Trim(addr, `"`), true
			}
		}
	}
	return "", false
}

// baseURL is how this machine reaches a server listening on addr.
func baseURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// probe bounds each health check, so one hung request cannot outlast readyWithin.
var probe = http.Client{Timeout: 2 * time.Second}

// healthy is why the server at base did not answer its health check with token, if it did not.
func healthy(t *testing.T, base, token string) error {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/grpc.health.v1.Health/Check", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := probe.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}
