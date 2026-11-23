package server

import (
	"bytes"
	"encoding/base64"
	"mike/backend/testutil/testgit"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// baselineRounds is how often each read is repeated for the figure. The smallest measurement is the one
// to believe: everything else running in the process only ever adds to the counter.
const baselineRounds = 9

// The figures behind the file routes' memory claims, measured by a command rather than by an argument.
//
// It asserts nothing about the numbers. A threshold this machine cannot meet under load is a flake, and
// docs/testing.md → Tests that fail only under load says what that costs. What it pins is that each read
// still answers, and it prints what each one allocates, so a change to those paths is compared against a
// printed figure rather than against a guess. Each figure includes one authenticated request's own
// machinery, so compare the halves against each other rather than reading one figure alone.
//
// Two things about the reading:
//
//   - The bodies are two corpora, because one is not a measurement. `source` has the redundancy of real
//     code and `base64` has the entropy of an attachment's bytes, which is the worst case a text read
//     can be handed. A compressor's cost moves with its window and only a little with the bytes, so the
//     two rows for one size should sit near each other; a ratio claim made on one corpus alone has been
//     wrong here once already.
//   - The `br` rows ask for what a browser asks for. Without Accept-Encoding the server never builds a
//     compressor, and that omission hides the single largest allocation on the path.
//
// The last figure is inflated by the harness: httptest.ResponseRecorder copies the body it is handed,
// which a real connection writing to a socket does not. Nothing else here is a harness artifact.
func TestFSReadAllocationBaseline(t *testing.T) {
	corpora := []struct {
		name string
		make func(int) []byte
	}{
		{"source ", compressibleBody},
		{"base64 ", incompressibleText},
	}
	for _, corpus := range corpora {
		for _, size := range []int{64 << 10, 900_000} {
			s := testServer(t)
			text := corpus.make(size)
			id, repo := committedProject(t, s, "p1", map[string][]byte{"notes.md": text})
			abs := "notes.md"
			head := strings.TrimSpace(testgit.GitOutput(t, repo, "rev-parse", "HEAD"))
			declareSuffixes(t, s, "viewer", true, "md")

			var working *httptest.ResponseRecorder
			report := func(half string, run func() *httptest.ResponseRecorder, want int) {
				warm, warmMallocs := measureBaseline(t, false, func() { run() })
				cold, coldMallocs := measureBaseline(t, true, func() { run() })
				rec := run()
				if rec.Code != want {
					t.Fatalf("%s/%s: status = %d, want %d: %s", corpus.name, half, rec.Code, want, rec.Body.String())
				}
				t.Logf("%s %7d B | %-24s body %7d B | warm %8d B (%4.1fx, %4d allocs) | cold %8d B (%4.1fx, %4d allocs)",
					corpus.name, size, half, rec.Body.Len(),
					warm, float64(warm)/float64(size), warmMallocs,
					cold, float64(cold)/float64(size), coldMallocs)
			}
			report("read, working tree", func() *httptest.ResponseRecorder {
				working = fsReadGetEncoded(t, s, id, abs, "", "")
				return working
			}, http.StatusOK)
			report("read, committed", func() *httptest.ResponseRecorder {
				return fsReadGetEncoded(t, s, id, abs, "HEAD", "")
			}, http.StatusOK)
			report("read, working tree, br", func() *httptest.ResponseRecorder {
				return fsReadGetEncoded(t, s, id, abs, "", "br")
			}, http.StatusOK)
			report("read, committed, br", func() *httptest.ResponseRecorder {
				return fsReadGetEncoded(t, s, id, abs, "HEAD", "br")
			}, http.StatusOK)
			report("bytes, working tree", func() *httptest.ResponseRecorder {
				return bytesGet(t, s, readQuery(id, "notes.md", "", ""))
			}, http.StatusOK)
			report("bytes, committed", func() *httptest.ResponseRecorder {
				return bytesGet(t, s, readQuery(id, "notes.md", "HEAD", ""))
			}, http.StatusOK)
			// The same committed bytes named by their object id. A pinned id is what lets the answer stream
			// out of git rather than be held so a refusal can still carry a status, so this row — not the
			// one above — is the figure the streaming half is judged on.
			report("bytes, committed by id", func() *httptest.ResponseRecorder {
				return bytesGet(t, s, readQuery(id, "notes.md", head, ""))
			}, http.StatusOK)

			// The same committed bytes, streamed, measured against a writer that keeps nothing. The rows
			// above are answered into httptest.ResponseRecorder, which accumulates whatever it is handed:
			// a body written in one Write costs it one buffer, and a body streamed in 32 KiB chunks costs it
			// the growth of one. That growth is the harness, not the route, so the streaming half is also
			// read here — where the figure is what a socket costs the server.
			// Two of these, because one figure of this kind is not a measurement: the working tree is the
			// streaming path this server has always had, and the committed object is the one it just grew.
			for _, row := range []struct {
				name  string
				query url.Values
			}{
				{"bytes, working tree, socket", readQuery(id, "notes.md", "", "")},
				{"bytes, committed, socket", readQuery(id, "notes.md", head, "")},
			} {
				ask := func() (int, int64) {
					return getToDiscard(t, s, readKindRequest(fsKindBytes, cloneQuery(row.query)))
				}
				dWarm, dWarmMallocs := measureBaseline(t, false, func() { ask() })
				dCold, dColdMallocs := measureBaseline(t, true, func() { ask() })
				code, written := ask()
				if code != http.StatusOK {
					t.Fatalf("%s/%s: status = %d, want 200", corpus.name, row.name, code)
				}
				t.Logf("%s %7d B | %-24s body %7d B | warm %8d B (%4.1fx, %4d allocs) | cold %8d B (%4.1fx, %4d allocs)",
					corpus.name, size, row.name, written,
					dWarm, float64(dWarm)/float64(size), dWarmMallocs,
					dCold, float64(dCold)/float64(size), dColdMallocs)
			}

			if committed := fsReadGetEncoded(t, s, id, abs, "HEAD", ""); !bytes.Equal(committed.Body.Bytes(), working.Body.Bytes()) {
				t.Error("the two halves of the text read answered different bytes for one file")
			}
		}
	}
}

// measureBaseline returns the smallest allocation any round saw, and the allocation count beside it.
//
// drain picks which of the two figures a route's cost is. A pooled buffer — the JSON encode buffer, or
// the compressor's own — survives one collection: a GC only moves a pool's contents from the per-P caches
// into the global one, where the next request can still take them; two collections drop them. So warm is
// what a server that has answered anything recently pays and cold is what the first request after a quiet
// period pays. On this path they differ by several megabytes for the same lines of code, and a claim that
// names one of them silently is worth less than the pair.
func measureBaseline(t *testing.T, drain bool, run func()) (uint64, int) {
	t.Helper()
	bestBytes, bestMallocs := ^uint64(0), 1<<30
	for i := 0; i < baselineRounds; i++ {
		runtime.GC()
		if drain {
			runtime.GC()
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		run()
		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew < bestBytes {
			bestBytes = grew
		}
		if count := int(after.Mallocs - before.Mallocs); count < bestMallocs {
			bestMallocs = count
		}
	}
	return bestBytes, bestMallocs
}

// incompressibleText is base64 of a deterministic high-entropy stream: what the text route is asked to
// open when a file is a data blob wearing a .txt suffix. It is still text the route will answer — no
// NUL and valid UTF-8 — and nothing in it compresses.
func incompressibleText(n int) []byte {
	raw := make([]byte, n/4*3+3)
	state := uint64(0x9E3779B97F4A7C15)
	for i := range raw {
		state = state*6364136223846793005 + 1442695040888963407
		raw[i] = byte(state >> 33)
	}
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(enc, raw)
	return enc[:n]
}

// compressibleBody is text with the redundancy of real source: repeated shapes, varying values.
func compressibleBody(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		b.WriteString("func Handler")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("(w http.ResponseWriter, r *http.Request) {\n\tif err := store.Get(")
		b.WriteString(strconv.Itoa(i * 7))
		b.WriteString("); err != nil {\n\t\tlogging.For(logging.HTTP).Errorf(\"failed: %v\", err)\n\t}\n}\n")
	}
	return b.Bytes()[:n]
}

// getToDiscard answers one request through the same middleware chain as testResponseAuth, into a writer
// that counts the bytes it is handed and keeps none of them — the way a connection behaves, and the way
// ResponseRecorder does not.
func getToDiscard(t *testing.T, s *Server, req *http.Request) (int, int64) {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrfToken)
	discard := &discardResponseWriter{header: http.Header{}}
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(discard, req)
	return discard.code, discard.written
}

type discardResponseWriter struct {
	header  http.Header
	code    int
	written int64
}

func (d *discardResponseWriter) Header() http.Header { return d.header }

func (d *discardResponseWriter) WriteHeader(code int) { d.code = code }

func (d *discardResponseWriter) Write(p []byte) (int, error) {
	d.written += int64(len(p))
	return len(p), nil
}

// cloneQuery copies a query so a row asked many times is asked the same question each time.
func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, vs := range q {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
