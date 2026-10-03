package ffmpeg

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
)

// loopback is one of ffmpeg's side outputs, carried over a local socket because os/exec passes no extra fds on Windows.
// Like the pipe it replaces, ffmpeg's writes block while castor is not reading, and the reader sees EOF once ffmpeg exits.
type loopback struct {
	url string

	// accepted closes once ffmpeg's connection is taken, or accepting was abandoned and conn stays nil.
	accepted chan struct{}
	conn     net.Conn

	stopAccepting func()
}

// newLoopback listens on an ephemeral loopback port and takes the first connection as ffmpeg's.
func newLoopback() (*loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("side output listener: %w", err)
	}
	l := &loopback{
		// The -progress feed places burned-in cues every 0.1s, so it must not wait in Nagle's buffer.
		url:           (&url.URL{Scheme: "tcp", Host: ln.Addr().String(), RawQuery: "tcp_nodelay=1"}).String(),
		accepted:      make(chan struct{}),
		stopAccepting: sync.OnceFunc(func() { _ = ln.Close() }),
	}
	go func() {
		defer close(l.accepted)
		conn, err := ln.Accept()
		l.stopAccepting()
		if err == nil {
			l.conn = conn
		}
	}()
	return l, nil
}

// Read waits for ffmpeg to connect; an ffmpeg that exits without ever opening the output reads as EOF.
func (l *loopback) Read(p []byte) (int, error) {
	<-l.accepted
	if l.conn == nil {
		return 0, io.EOF
	}
	n, err := l.conn.Read(p)
	if errors.Is(err, net.ErrClosed) {
		err = io.EOF
	}
	return n, err
}

func (l *loopback) Close() error {
	l.stopAccepting()
	<-l.accepted
	if l.conn == nil {
		return nil
	}
	if err := l.conn.Close(); !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
