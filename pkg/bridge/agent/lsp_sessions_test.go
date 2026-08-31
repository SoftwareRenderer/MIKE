package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/pkg/bridge/protocol"
)

// The sandbox is shared by every project, so one bridge connection holds one LSP
// session per workspace. These tests pin the properties that make that safe:
// sessions start and die on their own, each sees its own environment, and each
// one's notifications come back tagged with its own id.

// fakeLSPSource is a minimal language server: it answers any request with its
// FAKE_TAG and a per-process request counter, and emits one notification when
// asked. The counter is what proves a sibling session was not restarted, and the
// tag is what proves per-session env reached the right process.
//
// Written without backticks so it can live in a raw string here; decoding into a
// map keeps it free of struct tags for the same reason.
const fakeLSPSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func write(payload string) {
	fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
}

func main() {
	r := bufio.NewReader(os.Stdin)
	tag := os.Getenv("FAKE_TAG")
	n := 0
	for {
		length := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				length, _ = strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
			}
		}
		if length == 0 {
			continue
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		var msg map[string]interface{}
		_ = json.Unmarshal(buf, &msg)
		id := ""
		if v, ok := msg["id"]; ok && v != nil {
			if raw, err := json.Marshal(v); err == nil {
				id = string(raw)
			}
		}
		method, _ := msg["method"].(string)
		n++
		if method == "fake/notify" {
			write(fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"method\":\"win/notify\",\"params\":{\"tag\":%q}}", tag))
		}
		if id != "" {
			write(fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tag\":%q,\"n\":%d}}", id, tag, n))
		}
	}
}
`

// fakeLSPBin is built once for the whole test binary: the tests share it, so it
// must not live in any single test's TempDir (which is deleted when that test ends).
var fakeLSPBin string

var fakeLSPErr error

func TestMain(m *testing.M) {
	fakeLSPBin, fakeLSPErr = buildFakeLanguageServer()
	code := m.Run()
	if fakeLSPBin != "" {
		_ = os.RemoveAll(filepath.Dir(fakeLSPBin))
	}
	os.Exit(code)
}

// buildFakeLanguageServer compiles the fake server. Building a real binary is the
// same approach findBridgeBinary uses: a real process, no shell quoting, and no
// dependency beyond the Go toolchain already running these tests.
func buildFakeLanguageServer() (string, error) {
	dir, err := os.MkdirTemp("", "kanban-fakelsp-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeLSPSource), 0o644); err != nil {
		return "", err
	}

	pwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if err := os.Chdir(dir); err != nil {
		return "", err
	}
	defer os.Chdir(pwd)

	if out, err := exec.Command("go", "mod", "init", "fakelsp").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go mod init: %v (%s)", err, out)
	}
	bin := filepath.Join(dir, "fakelsp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v (%s)", err, out)
	}
	return bin, nil
}

// fakeLanguageServer returns the shared fake server, skipping the test if it
// could not be built (no toolchain in the environment).
func fakeLanguageServer(t *testing.T) string {
	t.Helper()
	if fakeLSPErr != nil {
		t.Skipf("fake language server unavailable: %v", fakeLSPErr)
	}
	return fakeLSPBin
}

// lspHarness drives a handler over pipes exactly like a real bridge connection
// and pumps everything the handler writes into one channel, because results and
// notifications share the stream.
type lspHarness struct {
	t       *testing.T
	h       *handler
	toAgent *io.PipeWriter
	frames  <-chan *protocol.Frame
}

func newLSPHarness(t *testing.T) *lspHarness {
	t.Helper()
	testToAgentPr, testToAgentPw := io.Pipe()
	agentToTestPr, agentToTestPw := io.Pipe()

	h := newHandler()
	h.closed.Store(false)

	done := make(chan struct{})
	go func() {
		_ = h.handleConn(&testPipeConn{reader: testToAgentPr, writer: agentToTestPw})
		close(done)
	}()

	frames := make(chan *protocol.Frame, 64)
	go func() {
		for {
			f, err := protocol.ReadFrame(agentToTestPr)
			if err != nil {
				close(frames)
				return
			}
			frames <- f
		}
	}()

	t.Cleanup(func() {
		h.close()
		_ = testToAgentPw.Close()
		_ = agentToTestPw.Close()
		<-done
	})

	return &lspHarness{t: t, h: h, toAgent: testToAgentPw, frames: frames}
}

func (hs *lspHarness) send(p protocol.LSPParams, id uint32) {
	hs.t.Helper()
	params, _ := json.Marshal(p)
	if err := protocol.WriteFrame(hs.toAgent, protocol.NewRequest(id, protocol.MethodLSP, params)); err != nil {
		hs.t.Fatalf("write lsp frame: %v", err)
	}
}

// nextOfType returns the next frame of one of the wanted types, skipping the
// others: notifications and results interleave, so a test that wants a result
// cannot assume it is next in the stream.
func (hs *lspHarness) nextOfType(types ...uint8) *protocol.Frame {
	hs.t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case f, ok := <-hs.frames:
			if !ok {
				hs.t.Fatal("bridge stream closed while waiting for a frame")
			}
			for _, want := range types {
				if f.Type == want {
					return f
				}
			}
		case <-timeout:
			hs.t.Fatalf("timed out waiting for frame type %v", types)
		}
	}
}

func (hs *lspHarness) start(session, binary, tag string) {
	hs.t.Helper()
	hs.send(protocol.LSPParams{Command: binary, Session: session, Env: []string{"FAKE_TAG=" + tag}}, 1)
	f := hs.nextOfType(protocol.TypeResult, protocol.TypeError)
	if f.Type != protocol.TypeResult {
		hs.t.Fatalf("LSPStart(%q) failed: %s", session, f.ErrorMsg())
	}
}

// lspResult decodes {"result":{"tag":…,"n":…}} out of a Result frame.
func lspResult(t *testing.T, f *protocol.Frame) (tag string, n int) {
	t.Helper()
	var outer struct {
		Result struct {
			Tag string `json:"tag"`
			N   int    `json:"n"`
		} `json:"result"`
	}
	if err := json.Unmarshal(f.Payload, &outer); err != nil {
		t.Fatalf("decode result %s: %v", f.Payload, err)
	}
	return outer.Result.Tag, outer.Result.N
}

func rawRequest(id int, method string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method})
	return raw
}

func rawNotification(method string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	return raw
}

// TestLSPSessionsCoexistWithOwnEnvironment is the point of the whole change: one
// sandbox, several projects, and each project's server keeps its own process and
// its own environment.
func TestLSPSessionsCoexistWithOwnEnvironment(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("ws-alpha", bin, "alpha")
	hs.start("ws-beta", bin, "beta")

	hs.send(protocol.LSPParams{Session: "ws-alpha", Raw: rawRequest(10, "fake/echo")}, 2)
	tag, n := lspResult(t, hs.nextOfType(protocol.TypeResult))
	if tag != "alpha" || n != 1 {
		t.Errorf("alpha answered tag=%q n=%d, want tag=alpha n=1", tag, n)
	}

	hs.send(protocol.LSPParams{Session: "ws-beta", Raw: rawRequest(11, "fake/echo")}, 3)
	tag, n = lspResult(t, hs.nextOfType(protocol.TypeResult))
	if tag != "beta" || n != 1 {
		t.Errorf("beta answered tag=%q n=%d, want tag=beta n=1", tag, n)
	}

	// A second request to beta must increment beta's own counter: if starting a
	// sibling had recycled it, n would be back to 1.
	hs.send(protocol.LSPParams{Session: "ws-beta", Raw: rawRequest(12, "fake/echo")}, 4)
	if _, n = lspResult(t, hs.nextOfType(protocol.TypeResult)); n != 2 {
		t.Errorf("beta second request n=%d, want 2 — its process was recycled by a sibling", n)
	}
}

// TestLSPNotificationsCarryTheirSession pins attribution: a diagnostic published
// by one workspace's server must not be handed to another.
func TestLSPNotificationsCarryTheirSession(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("ws-alpha", bin, "alpha")
	hs.start("ws-beta", bin, "beta")

	hs.send(protocol.LSPParams{Session: "ws-beta", Raw: rawNotification("fake/notify")}, 5)

	f := hs.nextOfType(protocol.TypeNotification)
	var notif protocol.LSPNotification
	if err := json.Unmarshal(f.Payload, &notif); err != nil {
		t.Fatalf("notification not enveloped: %v (%s)", err, f.Payload)
	}
	if notif.Session != "ws-beta" {
		t.Errorf("notification attributed to session %q, want ws-beta", notif.Session)
	}
	if !strings.Contains(string(notif.Raw), "win/notify") {
		t.Errorf("notification payload lost: %s", notif.Raw)
	}
}

// TestLSPStopTakesOnlyItsOwnSession: releasing a workspace must free its server
// and leave every other project running.
func TestLSPStopTakesOnlyItsOwnSession(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("ws-alpha", bin, "alpha")
	hs.start("ws-beta", bin, "beta")

	hs.send(protocol.LSPParams{Session: "ws-alpha", Stop: true}, 6)
	if f := hs.nextOfType(protocol.TypeResult, protocol.TypeError); f.Type != protocol.TypeResult {
		t.Fatalf("stop did not answer: %s", f.ErrorMsg())
	}
	if hs.h.session("ws-alpha") != nil {
		t.Error("stopped session is still registered")
	}
	if hs.h.session("ws-beta") == nil {
		t.Fatal("stopping one session removed another")
	}

	hs.send(protocol.LSPParams{Session: "ws-alpha", Raw: rawRequest(13, "fake/echo")}, 7)
	f := hs.nextOfType(protocol.TypeError, protocol.TypeResult)
	if f.Type != protocol.TypeError {
		t.Fatalf("request to a stopped session returned %v, want an error", f.Type)
	}
	if !strings.Contains(f.ErrorMsg(), "not started") {
		t.Errorf("error = %q, want it to say the session is not started", f.ErrorMsg())
	}

	hs.send(protocol.LSPParams{Session: "ws-beta", Raw: rawRequest(14, "fake/echo")}, 8)
	tag, n := lspResult(t, hs.nextOfType(protocol.TypeResult))
	if tag != "beta" || n != 1 {
		t.Errorf("beta after sibling stop: tag=%q n=%d, want beta/1", tag, n)
	}
}

// TestLSPUnknownSessionFailsCleanly: an addressed session that was never started
// gets a plain error rather than borrowing somebody else's server.
func TestLSPUnknownSessionFailsCleanly(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("ws-alpha", bin, "alpha")

	hs.send(protocol.LSPParams{Session: "ws-ghost", Raw: rawRequest(20, "fake/echo")}, 9)
	f := hs.nextOfType(protocol.TypeError, protocol.TypeResult)
	if f.Type != protocol.TypeError {
		t.Fatalf("unknown session returned %v (%s), want an error", f.Type, f.Payload)
	}
	if !strings.Contains(f.ErrorMsg(), "not started") {
		t.Errorf("error = %q, want it to say the session is not started", f.ErrorMsg())
	}

	hs.send(protocol.LSPParams{Session: "ws-alpha", Raw: rawRequest(21, "fake/echo")}, 10)
	tag, _ := lspResult(t, hs.nextOfType(protocol.TypeResult))
	if tag != "alpha" {
		t.Errorf("known session answered tag=%q, want alpha", tag)
	}
}

// TestLSPLegacySessionStillWorks keeps the older single-session contract alive: a
// host that addresses no session still gets a server, and its notifications come
// back with an empty id so the host can attribute them by elimination.
func TestLSPLegacySessionStillWorks(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("", bin, "legacy")

	hs.send(protocol.LSPParams{Raw: rawRequest(30, "fake/echo")}, 11)
	tag, _ := lspResult(t, hs.nextOfType(protocol.TypeResult))
	if tag != "legacy" {
		t.Errorf("legacy session answered tag=%q, want legacy", tag)
	}

	hs.send(protocol.LSPParams{Raw: rawNotification("fake/notify")}, 12)
	f := hs.nextOfType(protocol.TypeNotification)
	var notif protocol.LSPNotification
	if err := json.Unmarshal(f.Payload, &notif); err != nil || notif.Raw == nil {
		t.Fatalf("legacy notification malformed: %v (%s)", err, f.Payload)
	}
	if notif.Session != "" {
		t.Errorf("legacy notification carries session %q, want empty", notif.Session)
	}
}

// TestHandlerCloseTerminatesSessions: sessions must not outlive the connection
// that started them.
func TestHandlerCloseTerminatesSessions(t *testing.T) {
	bin := fakeLanguageServer(t)
	hs := newLSPHarness(t)

	hs.start("ws-alpha", bin, "alpha")
	hs.start("ws-beta", bin, "beta")

	hs.h.close()

	hs.h.mu.Lock()
	n := len(hs.h.lspSessions)
	hs.h.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions survived handler close, want 0", n)
	}
}
