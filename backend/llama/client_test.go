package llama

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/types"
)

// TestChatStream_ReadIdleTimeout verifies ChatStream returns
// context.DeadlineExceeded when the server stops sending data.
func TestChatStream_ReadIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
			return // unreachable, satisfies compiler
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	c.SetReadIdleTimeout(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected deadline exceeded error, got: %v", err)
	}
}

// TestChatStream_TimedOutRace closes the connection as the idle timer fires.
func TestChatStream_TimedOutRace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleRaceConn(t, conn)
		}
	}()

	c := NewLlamaClient("http://"+listener.Addr().String(), "test-model", "off", "")
	c.SetReadIdleTimeout(50 * time.Millisecond)

	for range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
		cancel()
	}

	listener.Close()
	wg.Wait()
}

func handleRaceConn(t *testing.T, conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4096)
	_, _ = conn.Read(buf)
	conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n"))
	conn.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
	time.Sleep(55 * time.Millisecond)
}

// TestChatStream_TimedOutDataRace exercises the timedOut race via io.Pipe.
func TestChatStream_TimedOutDataRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		testTimedOutRaceOnce(t)
	}
}

func testTimedOutRaceOnce(_ *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
		time.Sleep(50 * time.Millisecond)
		w.Close()
	}()

	scanner := bufio.NewScanner(r)
	idleTimeout := 50 * time.Millisecond
	var timedOut atomic.Bool
	readTimer := time.AfterFunc(idleTimeout, func() {
		timedOut.Store(true)
		r.Close()
	})
	readTimer.Stop()

	for scanner.Scan() {
		_ = scanner.Text()
		readTimer.Stop()
		readTimer.Reset(idleTimeout)
	}
	readTimer.Stop()
	_ = timedOut.Load()
}

// TestChatStream_ProductionTimedOutRace calls the real ChatStream
// and triggers the timedOut race by closing the connection at the deadline.
func TestChatStream_ProductionTimedOutRace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 8192)
				_, _ = c.Read(buf)
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\n"))
				c.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
				time.Sleep(50 * time.Millisecond)
			}(conn)
		}
	}()

	c := NewLlamaClient("http://"+listener.Addr().String(), "test-model", "off", "")
	c.SetReadIdleTimeout(50 * time.Millisecond)

	for range 200 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
		cancel()
	}

	listener.Close()
	wg.Wait()
}
