package main

// Tests for the gate itself. A ratchet has two failure modes and both are tested: it lets a new
// violation through, and it stops matching so that a real violation reads as clean. The second is
// the one that survives review, so every rule is pinned from both sides.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// withSmallMinGoFiles lets a fixture tree pass the anti-blindness guard. Production refuses a scan
// that sees fewer than minGoFiles files; a three-file fixture is exactly what the guard rejects, so
// detection tests opt out and one test below pins the guard itself.
func withSmallMinGoFiles(t *testing.T) {
	t.Helper()
	saved := minGoFiles
	minGoFiles = 1
	t.Cleanup(func() { minGoFiles = saved })
}

// writeFixture lays out a fake repo root from a path→content map.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func details(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Detail)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The baseline mechanism: both directions, because a stale baseline is the silent half.
// ---------------------------------------------------------------------------

func TestCompare_FlagsNewViolation(t *testing.T) {
	baseline := map[string]bool{"a.go|imports \"net/http\"": true}
	findings := []Finding{
		{File: "a.go", Detail: `imports "net/http"`},
		{File: "b.go", Detail: `imports "net/http"`},
	}
	cmp := Compare(findings, baseline)
	if len(cmp.New) != 1 || cmp.New[0].File != "b.go" {
		t.Fatalf("new violation not isolated: %+v", cmp.New)
	}
	if len(cmp.Stale) != 0 {
		t.Fatalf("nothing should be stale: %v", cmp.Stale)
	}
}

func TestCompare_FlagsStaleBaselineEntry(t *testing.T) {
	// The violation was fixed but the baseline was not regenerated: a gate that only looked for
	// additions would print "clean" and quietly keep room for the violation to come back.
	baseline := map[string]bool{"a.go|imports \"net/http\"": true}
	cmp := Compare([]Finding{}, baseline)
	if len(cmp.Stale) != 1 {
		t.Fatalf("stale baseline entry not reported: %+v", cmp)
	}
	if len(cmp.New) != 0 {
		t.Fatalf("no new violation expected: %v", cmp.New)
	}
}

func TestCompare_KeyIgnoresLineNumbers(t *testing.T) {
	// An edit above a violation moves its line and must not churn the baseline: churn gets
	// regenerated wholesale, and that is when the ratchet stops holding.
	baseline := map[string]bool{"a.go|keyed lock table stashMu": true}
	moved := []Finding{{File: "a.go", Line: 4000, Detail: "keyed lock table stashMu"}}
	if cmp := Compare(moved, baseline); len(cmp.New) != 0 || len(cmp.Stale) != 0 {
		t.Fatalf("line move must not change the verdict: %+v", cmp)
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, BaselinePath("demo"))
	findings := []Finding{
		{File: "z.go", Detail: "second"},
		{File: "a.go", Detail: "first"},
		{File: "a.go", Detail: "first"}, // duplicate: the same violation seen twice
	}
	if err := WriteBaseline(path, findings); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 deduplicated keys, got %d: %v", len(loaded), loaded)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "never by adding to it") {
		t.Errorf("baseline header must state its own rule, or someone will hand-edit it: %s", raw)
	}
	if cmp := Compare(findings, loaded); len(cmp.New) != 0 || len(cmp.Stale) != 0 {
		t.Errorf("a baseline written from findings must compare clean: %+v", cmp)
	}
}

// ---------------------------------------------------------------------------
// Each rule: a planted violation must be found, and a clean fixture must produce nothing.
// ---------------------------------------------------------------------------

func TestRuleHTTP(t *testing.T) {
	withSmallMinGoFiles(t)
	dirty := writeFixture(t, map[string]string{
		"backend/managers/h.go": "package managers\n\nimport (\n\t\"net/http\"\n)\n\nvar _ = http.StatusOK\n",
		"backend/server/ok.go":  "package server\n\nimport \"net/http\"\n\nvar _ = http.StatusOK\n",
		"backend/exec/clean.go": "package exec\n\nfunc Nop() {}\n",
	})
	findings, checked, err := scanHTTP(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if checked != 3 {
		t.Errorf("population wrong: %d", checked)
	}
	if len(findings) != 1 || findings[0].File != "backend/managers/h.go" {
		t.Fatalf("http violation in a domain package not found: %+v", findings)
	}
	if findings[0].Why == "" {
		t.Error("a new violation must explain itself, not just point at a file")
	}

	clean := writeFixture(t, map[string]string{
		"backend/server/ok.go":  "package server\n\nimport \"net/http\"\n\nvar _ = http.StatusOK\n",
		"backend/exec/clean.go": "package exec\n\nfunc Nop() {}\n",
	})
	if findings, _, err := scanHTTP(clean); err != nil || len(findings) != 0 {
		t.Fatalf("clean tree reported violations: %+v (%v)", findings, err)
	}
}

func TestRuleEnv(t *testing.T) {
	withSmallMinGoFiles(t)
	dirty := writeFixture(t, map[string]string{
		"backend/sandbox/dz.go": "package sandbox\n\nimport \"os\"\n\nfunc On() bool { return os.Getenv(\"DANGER_ZONE\") == \"1\" }\n",
		"backend/store/ok.go":   "package store\n\nfunc Nop() {}\n",
	})
	findings, _, err := scanEnv(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("call-time env read not found: %+v", findings)
	}
	// The key names the variable, so two variables in one file are two entries and cannot merge.
	if !strings.Contains(findings[0].Key(), "DANGER_ZONE") {
		t.Errorf("baseline key must name the variable, got %q", findings[0].Key())
	}

	clean := writeFixture(t, map[string]string{
		"backend/store/ok.go": "package store\n\nfunc Nop() {}\n",
		"main.go":             "package main\n\nimport \"os\"\n\nvar _ = os.Getenv(\"MIKE_PORT\")\n",
	})
	if findings, _, err := scanEnv(clean); err != nil || len(findings) != 0 {
		t.Fatalf("the resolution points themselves must be allowed: %+v (%v)", findings, err)
	}
}

func TestRuleLockTables(t *testing.T) {
	withSmallMinGoFiles(t)
	dirty := writeFixture(t, map[string]string{
		"backend/git/w.go":  "package git\n\ntype WorktreeManager struct {\n\tstashMu  sync.Map\n\tdiffCache sync.Map\n}\n",
		"backend/exec/e.go": "package exec\n\ntype TaskExecutor struct {\n\tmu sync.Mutex\n}\n",
	})
	findings, _, err := scanLockTables(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Detail, "stashMu") {
		t.Fatalf("keyed lock table not found: %+v", findings)
	}
	// A plain sync.Mutex and a non-lock sync.Map are not keyed tables; flagging them would bury the
	// six that matter, which is how a rule gets ignored.
	if contains(details(findings), "diffCache") || contains(details(findings), " mu ") {
		t.Fatalf("non-lock fields flagged: %+v", findings)
	}
}

func TestRuleRoutes_FindsEveryRegistrationShape(t *testing.T) {
	withSmallMinGoFiles(t)
	dirty := writeFixture(t, map[string]string{
		"backend/server/routes.go": `package server

func (s *Server) routes() {
	s.mux.HandleFunc("/api/inline", func(w any, r any) {})
	s.mux.HandleFunc("/api/manager", s.gitHandler.HandleCheckout)
	s.mux.HandleFunc("/api/local", s.handleAuthSession)
	s.mux.HandleFunc("POST /api/methoded", s.handlePost)
	s.mux.Handle("/api/plain", http.FileServer(nil))
}
`,
		"backend/server/server.go": `package server

import "kanban/backend/managers"

type Server struct {
	gitHandler *managers.GitHandler
}
`,
		"backend/managers/git.go": `package managers

import "net/http"

type GitHandler struct{}

func (h *GitHandler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	}
}
`,
	})
	findings, checked, err := scanRoutes(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if checked != 5 {
		t.Fatalf("expected 5 routes examined, got %d", checked)
	}
	got := details(findings)
	// Four unprefixed registrations, whatever the handler shape. A matcher that only understood
	// s.field.Method reported 44 missing methods where there were 51 in the real tree.
	for _, want := range []string{"/api/inline", "/api/manager", "/api/local", "/api/plain"} {
		if !contains(got, want) {
			t.Errorf("unprefixed route %s not reported: %v", want, got)
		}
	}
	if contains(got, "/api/methoded") {
		t.Errorf("a route that already carries a method must be clean: %v", got)
	}
	// The dispatch finding must name the handler resolved through the Server struct field, not
	// through a method-name guess.
	if !contains(got, "GitHandler.HandleCheckout dispatches on r.Method") {
		t.Errorf("receiver type not resolved from the Server field: %v", got)
	}
}

func TestMethodBody_FindsAMethodDeclaredInAnotherFile(t *testing.T) {
	withSmallMinGoFiles(t)
	// *Server's methods live in twenty files while the struct lives in one. Resolving a receiver to
	// its declaration file — the obvious implementation — finds nothing for almost every handler, and
	// the rules that read handler bodies then report a clean tree. This is the bug the plural
	// goFilesByType exists to prevent, so it is pinned, not just fixed.
	root := writeFixture(t, map[string]string{
		"backend/server/routes.go": `package server

func (s *Server) routes() {
	s.mux.HandleFunc("/api/messages", s.handleTaskMessages)
}
`,
		"backend/server/server.go": "package server\n\ntype Server struct{ mux any }\n",
		"backend/server/handler_message.go": `package server

import "net/http"

func (s *Server) handleTaskMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.deleteMessages(w, r)
	}
}
`,
	})
	findings, _, err := scanRoutes(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !contains(details(findings), "Server.handleTaskMessages dispatches on r.Method") {
		t.Fatalf("a method in a sibling file was invisible: %v", details(findings))
	}
}

func TestRuleRoutes_RefusesToParseFewerRoutesThanExist(t *testing.T) {
	withSmallMinGoFiles(t)
	// A registration shape the matcher does not know must stop the run. Reporting a smaller debt
	// than exists is the same false "done" the whole gate exists to prevent.
	root := writeFixture(t, map[string]string{
		"backend/server/routes.go": `package server

func (s *Server) routes() {
	s.mux.HandleFunc(registerThroughAVariable)
}
`,
		"backend/server/server.go": "package server\n\ntype Server struct{ mux any }\n",
	})
	_, _, err := scanRoutes(root)
	if err == nil || !strings.Contains(err.Error(), "the matcher is blind") {
		t.Fatalf("expected a blindness failure, got %v", err)
	}
}

func TestRuleWriteselect_FindsTheGETThatWrites(t *testing.T) {
	withSmallMinGoFiles(t)
	root := writeFixture(t, map[string]string{
		"backend/server/routes.go": `package server

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/projects/{id}/tasks/{id}/diff", s.gitHandler.HandleTaskDiff)
	s.mux.HandleFunc("GET /api/projects/{id}/board", s.boardHandler.HandleBoard)
	s.mux.HandleFunc("POST /api/projects/{id}/tasks", s.taskHandler.HandleList)
}
`,
		"backend/server/server.go": `package server

import "kanban/backend/managers"

type Server struct {
	gitHandler  *managers.GitHandler
	boardHandler *managers.BoardHandler
	taskHandler *managers.TaskHandler
}
`,
		"backend/managers/git.go": `package managers

import (
	"net/http"

	"kanban/backend/stream"
)

type GitHandler struct{ store any }

func (h *GitHandler) HandleTaskDiff(w http.ResponseWriter, r *http.Request) {
	stream.MaterializeDoneTaskDiff(nil, nil, "", nil)
	h.diffCache.Store("x", "y")
}
`,
		"backend/managers/board.go": `package managers

import "net/http"

type BoardHandler struct{ store any }

func (h *BoardHandler) HandleBoard(w http.ResponseWriter, r *http.Request) {
	h.store.GetProject("x")
}
`,
		"backend/managers/task.go": `package managers

import "net/http"

type TaskHandler struct{ store any }

func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	h.store.CreateTask(nil)
}
`,
	})
	findings, _, err := scanCancellableWrites(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := details(findings)
	// A store mutation on a GET route.
	if !contains(got, "MaterializeDoneTaskDiff") && !contains(got, "diffCache.Store") {
		t.Fatalf("the writable GET was not found: %v", got)
	}
	// A POST may write; that is the whole point of the rule's scoping. And a GET that only reads
	// must not be reported — the F19 first pass reported twelve, eleven of them wrong.
	if contains(got, "CreateTask") {
		t.Errorf("a POST write is not a cancellable-GET violation: %v", got)
	}
	if contains(got, "GetProject") {
		t.Errorf("a read-only GET was reported: %v", got)
	}
}

// unionFixture renders the frontend's declared list of app-stream frame names.
func unionFixture(names []string) string {
	out := "export type AppStreamEventType =\n"
	for i, n := range names {
		if i == 0 {
			out += "   '" + n + "'\n"
			continue
		}
		out += "    | '" + n + "'\n"
	}
	return out + ";\n"
}

// eventNames is the padded vocabulary the fixtures use: event_a … event_(n-1).
func eventNames(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "event_"+string(rune('a'+i)))
	}
	return out
}

func TestRuleWSEvents_FlagsABroadcastNobodyHeard(t *testing.T) {
	withSmallMinGoFiles(t)
	events := "package types\n\nconst (\n"
	for i := 0; i < 25; i++ {
		events += "\tWSTypeA" + string(rune('a'+i)) + " WSMessageType = \"event_" + string(rune('a'+i)) + "\"\n"
	}
	events += "\tWSTypeOrphan WSMessageType = \"orphan_event\"\n)\n"

	dirty := writeFixture(t, map[string]string{
		"backend/types/ws.go":            events,
		"frontend/src/api/app.ts":        "const t = \"event_a\";\n",
		"frontend/src/Panel.svelte":      "{#if msg.type === 'event_b'}x{/if}\n",
		"frontend/src/lib/api/events.ts": unionFixture(append(eventNames(25), "orphan_event")),
	})
	findings, checked, err := scanWSEvents(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if checked < 25 {
		t.Errorf("event vocabulary undercounted: %d", checked)
	}
	if len(findings) == 0 || !contains(details(findings), "orphan_event") {
		t.Fatalf("an unread broadcast must be reported: %+v", findings)
	}
	if contains(details(findings), "event_a") || contains(details(findings), "event_b") {
		t.Fatalf(".ts and .svelte readers both count as listeners: %+v", findings)
	}
}

func TestRuleWSEvents_RefusesATooSmallVocabulary(t *testing.T) {
	withSmallMinGoFiles(t)
	// The real vocabulary is two dozen constants. Finding three means the matcher stopped matching
	// the declaration shape — which is exactly what happened when the named type between the
	// identifier and the value was left out of the pattern.
	root := writeFixture(t, map[string]string{
		"backend/types/ws.go": "package types\n\nconst (\n\tWSTypeOne WSMessageType = \"one\"\n)\n",
		"frontend/src/app.ts": "const t = \"one\";\n",
	})
	if _, _, err := scanWSEvents(root); err == nil {
		t.Fatal("a collapsed vocabulary must fail the scan, not report clean")
	}
}

// The union is what makes the reverse direction answerable, so both directions are held: a frame the
// browser declares that Go never sends is a dispatcher branch that cannot run, and a Go name missing from
// the union is a frame the dispatcher's own type would reject.
func TestRuleWSEvents_UnionIsCheckedInBothDirections(t *testing.T) {
	withSmallMinGoFiles(t)
	consts := "package types\n\nconst (\n"
	for _, n := range eventNames(24) {
		consts += "\tWSType_" + n + " WSMessageType = \"" + n + "\"\n"
	}
	consts += "\tWSTypeOnlyGo WSMessageType = \"only_go\"\n)\n"

	root := writeFixture(t, map[string]string{
		"backend/types/ws.go": consts,
		// Every literal reader for the shared 24, so the only findings left are the two drifts.
		"frontend/src/api/app.ts":        "const t = \"event_a\";\n",
		"frontend/src/lib/api/events.ts": unionFixture(append(eventNames(24), "only_frontend")),
	})
	findings, checked, err := scanWSEvents(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if checked != 25+25 {
		t.Errorf("names examined = %d, want both lists (25 Go + 25 union)", checked)
	}
	got := details(findings)
	if !contains(got, "the frontend's union declares only_frontend, which no Go constant defines") {
		t.Errorf("an event the frontend invented was not reported: %v", got)
	}
	if !contains(got, "event only_go is missing from the frontend's AppStreamEventType union") {
		t.Errorf("a Go event absent from the union was not reported: %v", got)
	}
}

func TestRuleWSEvents_ASecondUnionDeclarationIsNotAReader(t *testing.T) {
	withSmallMinGoFiles(t)
	consts := "package types\n\nconst (\n"
	for _, n := range eventNames(24) {
		consts += "\tWSType_" + n + " WSMessageType = \"" + n + "\"\n"
	}
	consts += "\tWSTypeOnlyGo WSMessageType = \"only_go\"\n)\n"

	root := writeFixture(t, map[string]string{
		"backend/types/ws.go": consts,
		// The one real reader in the fixture — for the shared 24 only.
		"frontend/src/api/app.ts":        "const t = \"event_a\";\n",
		"frontend/src/lib/api/events.ts": unionFixture(append(eventNames(24), "only_go")),
		// The exec stream's second union declares only_go too, and nothing dispatches it. Were a
		// declaration counted as a reader, this fixture would look like a frontend that reads everything.
		"frontend/src/lib/types.ts": "export type StreamEventType =\n    | 'event_a'\n    | 'only_go'\n    ;\n\nexport function getToolDisplayName(n: string): string { return n }\n",
	})
	findings, _, err := scanWSEvents(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := details(findings)
	if !contains(got, "event only_go is never read by the frontend") {
		t.Errorf("a name declared in both unions and read by neither was excused: %v", got)
	}
	if contains(got, "event event_a is never read by the frontend") {
		t.Errorf("a name with a real reader was reported: %v", got)
	}
}

// A union the matcher cannot read must fail the scan: an empty list would otherwise look like a frontend
// that invented nothing.
func TestRuleWSEvents_RefusesAnUnreadableUnion(t *testing.T) {
	withSmallMinGoFiles(t)
	consts := "package types\n\nconst (\n"
	for _, n := range eventNames(24) {
		consts += "\tWSType_" + n + " WSMessageType = \"" + n + "\"\n"
	}
	consts += ")\n"
	root := writeFixture(t, map[string]string{
		"backend/types/ws.go":            consts,
		"frontend/src/api/app.ts":        "const t = \"event_a\";\n",
		"frontend/src/lib/api/events.ts": "export const somethingElse = 1;\n",
	})
	if _, _, err := scanWSEvents(root); err == nil {
		t.Fatal("an unreadable union must fail the scan, not report a clean contract")
	}
}

func TestRuleWSFrame_FramesAreNamedByTheVocabularyNotByHand(t *testing.T) {
	withSmallMinGoFiles(t)
	// goEvents refuses a vocabulary under 20 names, so the fixture carries a real one.
	vocab := "package types\n\nconst (\n\tWSTypeStatus WSMessageType = \"status\"\n"
	for i := 0; i < 24; i++ {
		vocab += "\tWSTypePad" + string(rune('a'+i)) + " WSMessageType = \"pad_" + string(rune('a'+i)) + "\"\n"
	}
	vocab += ")\n"
	root := writeFixture(t, map[string]string{
		"backend/types/sse.go": "package types\n\nconst (\n\tSSETypeStatus SSEMessageType = \"status\"\n\tSSETypeProgress SSEMessageType = \"progress\"\n\tSSETypeProgress SSEMessageType = \"progress\"\n\tSSETypeDone SSEMessageType = \"done\"\n)\n",
		"backend/types/ws.go":  vocab,
		"backend/stream/app_stream.go": `package stream

func known() map[string]any   { return map[string]any{"type": "status", "task_id": id} }
func unknown() map[string]any { return map[string]any{"type": "brand_new_event"} }
func sealed() map[string]any  { return appFrame(types.WSTypeStatus, nil) }
func shortcut() map[string]any { return appFrame("status", nil) }
`,
		// The clone stream is its own protocol with its own vocabulary. Its handler builds frames too,
		// so it is a home of this rule — and the finding must name SSEType, not the WebSocket's names.
		"backend/managers/project.go": `package managers

func c() map[string]any { return map[string]any{"type": "progress"} }
`,
	})
	findings, checked, err := scanWSFrameLiterals(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Four frames built in the stream package — two inline, one sealed, one shortcut — and the SSE
	// site outside the package is not part of the population at all.
	if checked != 5 {
		t.Errorf("frames examined = %d, want 5 (four app-stream, one clone stream)", checked)
	}
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want three inline names and the shortcut", findings)
	}
	var sseNamed int
	for _, f := range findings {
		if strings.Contains(f.File, "managers") && strings.Contains(f.Detail, "types.SSEType constant") {
			sseNamed++
		}
	}
	if sseNamed != 1 {
		t.Errorf("a clone-stream frame must be reported against its own vocabulary: %+v", findings)
	}
	var secondSpelling, unseen, builder int
	for _, f := range findings {
		switch {
		case strings.Contains(f.Why, "second spelling"):
			secondSpelling++
		case strings.Contains(f.Why, "parity rule never sees it"):
			unseen++
		case strings.Contains(f.Detail, "appFrame is given a string"):
			builder++
		}
	}
	// Two of the four are "a constant exists and was not used" — one per protocol, since each home is
	// named against its own vocabulary.
	if secondSpelling != 2 || unseen != 1 || builder != 1 {
		t.Errorf("the three shapes must be told apart — a name the vocabulary owns is not a name it has "+
			"never heard: %+v", findings)
	}
}

// A rule whose population is "literals found" reports a fixed tree as a broken matcher, so the population
// is frames built. With no frame in the scanned package at all, the only honest answer is that the matcher
// stopped matching.
// An event emitted from outside a frame home is the escape hatch this rule used to have: the name never
// reached the vocabulary, so the parity rule silently stopped comparing it.
func TestRuleWSFrame_FlagsAnEventNamedOutsideAFrameHome(t *testing.T) {
	withSmallMinGoFiles(t)
	vocab := "package types\n\nconst (\n\tWSTypeStatus WSMessageType = \"status\"\n"
	for i := 0; i < 24; i++ {
		vocab += "\tWSTypePad" + string(rune('a'+i)) + " WSMessageType = \"pad_" + string(rune('a'+i)) + "\"\n"
	}
	vocab += ")\n"
	root := writeFixture(t, map[string]string{
		"backend/types/ws.go":          vocab,
		"backend/types/sse.go":         "package types\n\nconst (\n\tSSETypeStatus SSEMessageType = \"status\"\n\tSSETypeDone SSEMessageType = \"done\"\n)\n",
		"backend/stream/app_stream.go": "package stream\n\nfunc f() map[string]any { return appFrame(types.WSTypeStatus, nil) }\n",
		"backend/exec/summarize.go": `package exec

func a() { send(types.StreamEvent{Type: "brand_new_event"}) }
func b() { send(types.StreamEvent{Type: types.WSTypeStatus}) }
`,
	})
	findings, checked, err := scanWSFrameLiterals(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !contains(details(findings), `event "brand_new_event" is emitted by a literal StreamEvent Type, not a types.WSType constant`) {
		t.Errorf("an event named outside a frame home was not reported: %v", details(findings))
	}
	// The sealed sibling must not be reported, and both shapes count toward the population.
	if len(findings) != 1 {
		t.Errorf("findings = %v, want only the literal", details(findings))
	}
	if checked < 2 {
		t.Errorf("population = %d, want the sealed and the literal shape both counted", checked)
	}
}

func TestRuleWSFrame_RefusesATreeWithNoFramesAtAll(t *testing.T) {
	withSmallMinGoFiles(t)
	root := writeFixture(t, map[string]string{
		"backend/types/ws.go":          "package types\n\nconst (\n\tWSTypeStatus WSMessageType = \"status\"\n)\n",
		"backend/stream/app_stream.go": "package stream\n\nfunc nothing() int { return 0 }\n",
	})
	if _, _, err := scanWSFrameLiterals(root); err == nil {
		t.Fatal("a scan that found no frame must fail, not print a clean count")
	}
}

// ---------------------------------------------------------------------------
// Wiring and the walker guard.
// ---------------------------------------------------------------------------

func TestGoFiles_RefusesABrokenWalk(t *testing.T) {
	root := writeFixture(t, map[string]string{"backend/one.go": "package backend\n"})
	if _, err := goFiles(root); err == nil || !strings.Contains(err.Error(), "refusing to report clean") {
		t.Fatalf("a walk that finds one file must fail, not report clean: %v", err)
	}
}

func TestGoFiles_SkipsWorktreesAndTestFiles(t *testing.T) {
	withSmallMinGoFiles(t)
	root := writeFixture(t, map[string]string{
		"backend/a.go":                   "package backend\n",
		"backend/a_test.go":              "package backend\n",
		"backend/.worktrees/x/b.go":      "package x\n",
		"backend/testdata/c.go":          "package c\n",
		"frontend/src/node_modules/d.go": "package d\n",
		"main.go":                        "package main\n",
	})
	files, err := goFiles(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	got := strings.Join(files, ",")
	if strings.Contains(got, "_test.go") || strings.Contains(got, ".worktrees") ||
		strings.Contains(got, "testdata") || strings.Contains(got, "node_modules") {
		t.Fatalf("scanner walked a tree it must skip: %v", files)
	}
	if len(files) != 2 {
		t.Fatalf("expected backend/a.go and main.go, got %v", files)
	}
}

func TestSelectRules_UnknownNameIsAnError(t *testing.T) {
	// A typo in `make test-archgate -rule envv` must not run zero rules and exit 0.
	if _, err := selectRules(rules, "envv"); err == nil {
		t.Fatal("unknown rule name accepted")
	}
	if got, err := selectRules(rules, ""); err != nil || len(got) != len(rules) {
		t.Fatalf("empty selection must mean all rules: %v %v", got, err)
	}
}

// -v is how a baselined violation gets fixed, so it has to say where it is. A listing of keys alone
// sends the reader to grep, and a violation they cannot locate is a violation that stays baselined.
func TestRun_VerboseListsBaselinedViolationsWithTheirLocation(t *testing.T) {
	withSmallMinGoFiles(t)
	dir := writeFixture(t, map[string]string{
		"backend/echo.go": "package exec\n\nimport \"net/http\"\n\nfunc h(w http.ResponseWriter) {\n" +
			"\tJSONError(w, http.StatusInternalServerError, err.Error())\n}\n",
		"backend/other.go":                        "package exec\n\nfunc unused() {}\n",
		"scripts/archgate/baseline/status500.txt": "backend/echo.go|500 echoes err\n",
	})

	out := captureStdout(t, func() { run(Options{Rule: "status500", Verbose: true, Root: dir}) })
	if !regexp.MustCompile(`backend/echo\.go:[0-9]+`).MatchString(out) {
		t.Errorf("-v must print file:line so a baselined violation can be opened; got:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(r)
		done <- string(raw)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestRun_IsIdempotentAgainstTheRealTree(t *testing.T) {
	root := repoRoot(t)
	for _, rule := range rules {
		findings, checked, err := rule.Scan(root)
		if err != nil {
			t.Fatalf("%s: %v", rule.Name, err)
		}
		if checked == 0 {
			t.Fatalf("%s examined nothing", rule.Name)
		}
		baselinePath := filepath.Join(root, BaselinePath(rule.Name))
		baseline, err := LoadBaseline(baselinePath)
		if err != nil {
			t.Fatalf("%s: no baseline at %s (%v) — run -update", rule.Name, baselinePath, err)
		}
		if cmp := Compare(findings, baseline); len(cmp.New) != 0 || len(cmp.Stale) != 0 {
			t.Errorf("%s: baseline out of date (new=%d stale=%d) — re-run -update and read the diff",
				rule.Name, len(cmp.New), len(cmp.Stale))
		}
	}
}

// countRegistrations is the independent grep: it counts the registration call, not the pattern shape,
// so it cannot share a bug with the scanner's matcher.
func countRegistrations(t *testing.T, root string) int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "backend", "server", "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	return len(regexp.MustCompile(`s\.mux\.Handle`).FindAllIndex(body, -1))
}

// TestMeasuredNumbersAgreeWithTheReview locks the tool to the Phase 0 measurements, so a matcher that
// drifts away from the tree it describes fails here rather than shrinking the register silently.
// The numbers and where they diverge from the original claims are in TODO.md, "0. Validate the findings".
func TestMeasuredNumbersAgreeWithTheReview(t *testing.T) {
	root := repoRoot(t)

	// The count `grep -c 's\.mux\.Handle' backend/server/routes.go` answers today: 107. The verb
	// split is 45 GET, 39 POST, 10 DELETE, 7 PUT, 4 PATCH and 2 methodless — the two file servers,
	// left as subtrees on purpose. A new registration moves this number and nothing else here.
	const routeRegistrations = 107
	if routes, checked, err := scanRoutes(root); err != nil {
		t.Fatalf("scanRoutes: %v", err)
	} else if checked != routeRegistrations {
		t.Fatalf("routes.go holds %d registrations by this test's count; the scanner examined %d", routeRegistrations, checked)
	} else if n := countRegistrations(t, root); n != checked {
		t.Fatalf("the file has %d s.mux.Handle lines but the scanner counted %d — the matcher is blind to a shape", n, checked)
	} else if len(routes) == 0 {
		t.Fatal("no route findings at all")
	}

	// Phase 0 measured 7: six in backend/ (git's three, exec's, server's two) plus
	// pkg/bridge/agent's, which a grep over backend/ could not see. Phase 2b folded the six into
	// pkg/keylock, so the rule should now find exactly the bridge's — a different process, stdio-only
	// lifetime, and migrating it would re-sign a committed binary for no behavioural gain, which is why
	// it stays a baselined exception rather than a silent absence. A new hand-rolled table lands here.
	tables, _, err := scanLockTables(root)
	if err != nil {
		t.Fatalf("locks: %v", err)
	}
	if len(tables) != 1 || !strings.Contains(details(tables)[0], "fileLocks") {
		t.Fatalf("expected only pkg/bridge/agent's fileLocks to be a hand-rolled keyed table, got %d: %v",
			len(tables), details(tables))
	}

	// F19: exactly one GET writes state, and it is the done-diff snapshot.
	writes, _, err := scanCancellableWrites(root)
	if err != nil {
		t.Fatalf("writeselect: %v", err)
	}
	if len(writes) == 0 || !contains(details(writes), "MaterializeDoneTaskDiff") {
		t.Fatalf("the one writable GET is %s: %v", "GET .../tasks/{id}/diff", details(writes))
	}

	// F9: no app-stream event is unreferenced by the frontend.
	events, vocabulary, err := scanWSEvents(root)
	if err != nil {
		t.Fatalf("wsevents: %v", err)
	}
	if vocabulary < 20 {
		t.Fatalf("app-stream vocabulary shrank to %d constants", vocabulary)
	}
	if len(events) != 0 {
		t.Fatalf("drift appeared between the Go events and the frontend: %v", details(events))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Tests run with the package directory as the working directory.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backend", "server", "routes.go")); err != nil {
		t.Skip("not running from the repository")
	}
	return root
}

// --- branch-aware GET selection (writeselect) --------------------------------
//
// A handler registered for GET+POST+DELETE reports the writes in its POST branch as GET writes
// unless the rule follows the dispatch. 22 such false accusations appeared the moment the route table
// gained method prefixes, which is exactly the noise that makes a team ignore a gate — so the shapes
// are pinned individually, including the one where a GET write must still be caught.

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	// The population guard would refuse a synthetic tree, correctly: it exists so a matcher that
	// walked nothing cannot print a clean bill. Lowering it here is what it is a var for.
	saved := minGoFiles
	minGoFiles = 1
	t.Cleanup(func() { minGoFiles = saved })
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// dispatcherTree is a minimal backend with one handler serving three verbs, written in `shape`.
func dispatcherTree(t *testing.T, shape, where string) string {
	t.Helper()
	var dispatch string
	switch shape {
	case "switch":
		dispatch = `	switch r.Method {
	case http.MethodPost:
		s.store.CreateTask(nil)
		return
	case http.MethodDelete:
		s.store.DeleteTask(nil)
		return
	case http.MethodGet:
` + where + `
		return
	}
`
	case "if":
		dispatch = `	if r.Method == http.MethodPost {
		s.store.CreateTask(nil)
		return
	}
	if r.Method == http.MethodGet {
` + where + `
		return
	}
`
	case "guard":
		dispatch = `	if r.Method == http.MethodPost {
		s.store.CreateTask(nil)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "nope", http.StatusMethodNotAllowed)
		return
	}
` + where + `
`
	}
	return writeTree(t, map[string]string{
		"backend/server/server.go": `package server

type Server struct {
	store *store.Store
}
`,
		"backend/server/routes.go": `package server

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/projects/{projectID}/tasks", s.handleTasks)
	s.mux.HandleFunc("POST /api/projects/{projectID}/tasks", s.handleTasks)
	s.mux.HandleFunc("DELETE /api/projects/{projectID}/tasks", s.handleTasks)
}
`,
		"backend/server/handler.go": `package server

import "net/http"

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
` + dispatch + `}
`,
	})
}

func writeFindings(t *testing.T, root string) []string {
	t.Helper()
	findings, _, err := scanCancellableWrites(root)
	if err != nil {
		t.Fatalf("scanCancellableWrites: %v", err)
	}
	var out []string
	for _, f := range findings {
		out = append(out, f.Detail)
	}
	return out
}

// A write in a non-GET branch must never be reported as a GET write, in any dispatch shape.
func TestWriteselectIgnoresWritesInOtherVerbBranches(t *testing.T) {
	for _, shape := range []string{"switch", "if", "guard"} {
		t.Run(shape, func(t *testing.T) {
			// `where` is a line in the GET path that does not write.
			root := dispatcherTree(t, shape, "	_ = r")
			for _, d := range writeFindings(t, root) {
				if strings.Contains(d, "CreateTask") || strings.Contains(d, "DeleteTask") {
					t.Errorf("%s shape: reported another verb's write: %s", shape, d)
				}
			}
		})
	}
}

// The same tree, with the write moved into the GET path: the rule must find it. Without this the
// ignore-test above could pass because branch extraction returns nothing at all.
func TestWriteselectCatchesAWriteInTheGETBranch(t *testing.T) {
	for _, shape := range []string{"switch", "if", "guard"} {
		t.Run(shape, func(t *testing.T) {
			root := dispatcherTree(t, shape, "	s.store.UpsertTask(nil)")
			var found bool
			for _, d := range writeFindings(t, root) {
				if strings.Contains(d, "UpsertTask") {
					found = true
				}
			}
			if !found {
				t.Errorf("%s shape: a write in the GET branch went unreported (findings=%v)", shape, writeFindings(t, root))
			}
		})
	}
}

// A multi-verb handler with no recognisable dispatch is reported rather than passed: "cannot tell"
// must not read as "clean".
func TestWriteselectReportsAHandlerWhoseGETBranchItCannotFind(t *testing.T) {
	root := writeTree(t, map[string]string{
		"backend/server/server.go": "package server\n\ntype Server struct {\n\tstore *store.Store\n}\n",
		"backend/server/routes.go": `package server

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/odd", s.handleOdd)
	s.mux.HandleFunc("POST /api/odd", s.handleOdd)
}
`,
		"backend/server/handler.go": `package server

import "net/http"

func (s *Server) handleOdd(w http.ResponseWriter, r *http.Request) {
	dispatch(w, r, s.store)
}
`,
	})
	findings := writeFindings(t, root)
	if len(findings) == 0 {
		t.Fatal("a dispatcher the rule cannot read was reported as clean")
	}
	if !strings.Contains(findings[0], "no identifiable GET branch") {
		t.Errorf("expected the cannot-tell finding, got %q", findings[0])
	}
}

// A closure written in routes.go serves exactly the method it was written for. Four closures on one
// path (GET/PUT/PATCH/DELETE on /api/extensions/{id}) share nothing but the path, and keying them by
// path told the rule that each body answers four verbs.
func TestWriteselectTreatsEachInlineClosureAsItsOwnVerb(t *testing.T) {
	root := writeTree(t, map[string]string{
		"backend/server/server.go": "package server\n\ntype Server struct {\n\textHandler *extensions.ExtensionHandler\n}\n",
		"backend/server/routes.go": `package server

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleGet(w, r)
	})
	s.mux.HandleFunc("PUT /api/extensions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.extHandler.HandleUpdate(w, r)
	})
}
`,
	})
	if findings := writeFindings(t, root); len(findings) != 0 {
		t.Fatalf("separate per-method closures reported %v", findings)
	}
}

// --- envassign ---------------------------------------------------------------
//
// The scar behind this rule is real: `cmd.Env = os.Environ()` on an agent subprocess leaked
// MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY to an extension-supplied binary. The rule is a hard
// gate with an empty baseline, so both directions have to be proven.

func TestEnvAssign_CatchesAWholeEnvironment(t *testing.T) {
	root := writeTree(t, map[string]string{
		"backend/exec/leaky.go": `package exec

func run() {
	cmd.Env = os.Environ()
}
`,
	})
	findings, checked, err := scanEnvAssignment(root)
	if err != nil {
		t.Fatalf("scanEnvAssignment: %v", err)
	}
	if checked != 1 {
		t.Errorf("examined %d assignments, want 1", checked)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Detail, "os.Environ()") {
		t.Fatalf("a whole-environment assignment went unreported: %v", details(findings))
	}
}

// The sanctioned shapes must pass, including the ones that look like hand-built environments: git
// rebuilds its own for a clone, and the terminal widens deliberately.
func TestEnvAssign_AllowsTheAllowlistBuilders(t *testing.T) {
	root := writeTree(t, map[string]string{
		"backend/a/one.go":   "package a\n\nfunc f(c *exec.Cmd) {\n\tc.Env = Env(\"GIT_DIR=/x\")\n}\n",
		"backend/a/two.go":   "package a\n\nfunc g(c *exec.Cmd) {\n\tc.Env = git.CloneEnv()\n}\n",
		"backend/a/three.go": "package a\n\nfunc h(c *exec.Cmd) {\n\tc.Env = sandboxruntime.PodmanEnv()\n}\n",
		"backend/a/four.go":  "package a\n\nfunc i(c *exec.Cmd) {\n\tc.Env = agent.SafeAgentEnv(\"X=1\")\n}\n",
		"backend/a/five.go":  "package a\n\nfunc j(c *exec.Cmd) {\n\tc.Env = hostShellEnv()\n}\n",
		"backend/a/six.go":   "package a\n\nfunc k(c *exec.Cmd) {\n\tsubproc.Bind(c, PodmanEnv(), d)\n}\n",
	})
	findings, checked, err := scanEnvAssignment(root)
	if err != nil {
		t.Fatalf("scanEnvAssignment: %v", err)
	}
	// Five, not six: the `subproc.Bind` line assigns nothing at the call site, which is the reason
	// the helper exists — a caller that cannot name cmd.Env cannot drop the allowlist either.
	if checked != 5 {
		t.Errorf("examined %d assignments, want 5", checked)
	}
	if len(findings) != 0 {
		t.Errorf("a sanctioned builder was reported: %v", details(findings))
	}
}

// A rule whose matcher stopped matching prints the same clean line as a tree with no leaks.
func TestEnvAssign_RefusesToReportCleanWhenItSeesNothing(t *testing.T) {
	root := writeTree(t, map[string]string{"backend/a/quiet.go": "package a\n\nfunc f() {}\n"})
	if _, _, err := scanEnvAssignment(root); err == nil {
		t.Fatal("the rule reported clean over a tree with no cmd.Env assignment at all")
	}
}

// The 500 rule must find the echo in both shapes it appears in, and must not flag the two shapes that are
// deliberate: a fixed "internal error", and a 4xx whose sentence was written for the caller.
func TestRuleStatus500(t *testing.T) {
	withSmallMinGoFiles(t)
	dirty := writeFixture(t, map[string]string{
		"backend/managers/a.go": "package managers\n\n" +
			"func (h *H) A() {\n\thtputil.JSONError(w, http.StatusInternalServerError, err.Error())\n}\n\n" +
			"func (h *H) B() {\n\thtputil.JSONError(w, http.StatusInternalServerError,\n\t\tfmt.Sprintf(\"get children: %v\", err))\n}\n",
		"backend/managers/b.go": "package managers\n\n" +
			"func (h *H) C() {\n\thtputil.JSONError(w, http.StatusInternalServerError, \"internal error\")\n}\n\n" +
			"func (h *H) D() {\n\thtputil.JSONError(w, http.StatusBadRequest, fmt.Sprintf(\"invalid path %v\", err))\n}\n\n" +
			"func (h *H) E() {\n\thtputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf(\"limit %v\", limit))\n}\n",
	})
	findings, checked, err := scanStatusEcho(dirty)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("want the two echoes (same-line and continued), got %+v", findings)
	}
	if checked != 5 {
		t.Errorf("population = %d, want 5 status responses examined", checked)
	}
	for _, f := range findings {
		if !strings.Contains(f.Detail, "err") {
			t.Errorf("detail %q must name the echoed value", f.Detail)
		}
	}
}

// A rule that stopped matching prints the same "0 violations" as a codebase that fixed everything, so the
// population is asserted separately from the count.
func TestRuleStatus500_ReportsItsPopulation(t *testing.T) {
	withSmallMinGoFiles(t)
	clean := writeFixture(t, map[string]string{
		"backend/managers/a.go": "package managers\n\nfunc (h *H) A() {\n\thtputil.Fail(w, err, \"get task\", \"task not found\")\n}\n",
	})
	findings, checked, err := scanStatusEcho(clean)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("httputil.Fail is the answer this rule asks for, so it must not be flagged: %+v", findings)
	}
	if checked != 0 {
		t.Fatalf("population = %d, want 0 for a tree with no JSONError call", checked)
	}
	if _, _, err := scanStatusEcho(repoRoot(t)); err != nil {
		t.Fatalf("scan the real tree: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ctxroot: the register of base contexts. Both blindness modes are pinned, and so is the
// subtler one this rule was rewritten to catch — a second context.Background() inside a function
// that is already classified.
// ---------------------------------------------------------------------------

func TestRuleCtxRoot_CountsSitesAndFlagsUnclassified(t *testing.T) {
	withSmallMinGoFiles(t)
	root := writeFixture(t, map[string]string{
		"backend/a/a.go": `package a

import "context"

func Root() { _ = context.Background() }

func TwoSites() {
	ctx := context.Background()
	_ = ctx
	_ = context.TODO()
}

func Unclassified() { _ = context.Background() }
`,
		"backend/a/b.go": `package a

type T struct{}

func (T) Method() { _ = context.Background() }
`,
	})
	baseline := []string{
		"backend/a/a.go|Root|root|x1",
		"backend/a/a.go|TwoSites|detached|x2",
		"backend/a/b.go|T.Method|root|x1",
	}
	if err := WriteBaseline(filepath.Join(root, BaselinePath("ctxroot")), toFindings(baseline)); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	findings, checked, err := scanContextRoots(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if checked == 0 {
		t.Fatal("no files examined")
	}
	cmp := Compare(findings, mustBaseline(t, filepath.Join(root, BaselinePath("ctxroot"))))
	if len(cmp.New) != 1 || !strings.Contains(cmp.New[0].Detail, "Unclassified|unclassified|x1") {
		t.Fatalf("want exactly the unclassified site flagged, got new=%v", details(cmp.New))
	}
	if !strings.Contains(cmp.New[0].Why, "root|detached|legacy") {
		t.Errorf("the finding must name the vocabulary it needs, got %q", cmp.New[0].Why)
	}
	if len(cmp.Stale) != 0 {
		t.Fatalf("classified sites must keep their entries alive, got stale=%v", cmp.Stale)
	}
	// The count is part of the identity: TwoSites has two base contexts and its entry says x2.
	if !contains(details(findings), "TwoSites|detached|x2") {
		t.Fatalf("want the two-site function reported with its count, got %v", details(findings))
	}
}

func TestRuleCtxRoot_OneMoreContextInAClassifiedFunctionIsAFinding(t *testing.T) {
	withSmallMinGoFiles(t)
	write := func(body string) string {
		return writeFixture(t, map[string]string{
			"backend/a/a.go": "package a\n\nimport \"context\"\n\nfunc Root() {\n" + body + "}\n",
		})
	}
	root := write("\t_ = context.Background()\n")
	if err := WriteBaseline(filepath.Join(root, BaselinePath("ctxroot")),
		toFindings([]string{"backend/a/a.go|Root|root|x1"})); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	clean := func(r string) Comparison {
		t.Helper()
		f, _, err := scanContextRoots(r)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return Compare(f, mustBaseline(t, filepath.Join(r, BaselinePath("ctxroot"))))
	}
	if cmp := clean(root); len(cmp.New) != 0 || len(cmp.Stale) != 0 {
		t.Fatalf("fixture must start clean: new=%v stale=%v", details(cmp.New), cmp.Stale)
	}

	grown := write("\t_ = context.Background()\n\t_ = context.Background()\n")
	if err := WriteBaseline(filepath.Join(grown, BaselinePath("ctxroot")),
		toFindings([]string{"backend/a/a.go|Root|root|x1"})); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cmp := clean(grown)
	if len(cmp.New) != 1 || !strings.Contains(cmp.New[0].Detail, "|x2") {
		t.Fatalf("a second base context in a classified function must be a finding, got new=%v", details(cmp.New))
	}
	if len(cmp.Stale) != 1 {
		t.Fatalf("and its x1 entry must read as stale, got %v", cmp.Stale)
	}
}

func TestRuleCtxRoot_RefusesAMalformedRegister(t *testing.T) {
	withSmallMinGoFiles(t)
	for name, line := range map[string]string{
		"missing count":  "backend/a/a.go|Root|root",
		"unknown reason": "backend/a/a.go|Root|because|x1",
		"wrong shape":    "backend/a/a.go|root|x1|extra",
	} {
		t.Run(name, func(t *testing.T) {
			root := writeFixture(t, map[string]string{
				"backend/a/a.go": "package a\n\nimport \"context\"\n\nfunc Root() { _ = context.Background() }\n",
			})
			path := filepath.Join(root, BaselinePath("ctxroot"))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := scanContextRoots(root); err == nil {
				t.Fatalf("entry %q must refuse the run — a register that accepts a line it cannot read "+
					"will silently stop classifying the site it names", line)
			}
		})
	}
}

func TestCtxRootRealRegister_IsCompleteAndNamesRealFunctions(t *testing.T) {
	root := repoRoot(t)
	findings, _, err := scanContextRoots(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) < 30 {
		t.Fatalf("the register holds %d entries; Phase 0 measured 46 base contexts across the tree, so a "+
			"count this small means the matcher stopped seeing them", len(findings))
	}
	for _, f := range findings {
		if strings.Contains(f.Detail, "unclassified") {
			t.Errorf("%s is not classified — every base context must be root, detached or legacy", f.Location())
		}
		if strings.Contains(f.Detail, "<file>") {
			t.Errorf("%s: a base context outside any function body is reported as <file>; the register must name "+
				"where it lives", f.Location())
		}
	}
	var legacy, rootCtx, detached int
	for _, f := range findings {
		switch {
		case strings.Contains(f.Detail, "|legacy|"):
			legacy++
		case strings.Contains(f.Detail, "|root|"):
			rootCtx++
		case strings.Contains(f.Detail, "|detached|"):
			detached++
		}
	}
	if legacy == 0 {
		t.Fatal("no legacy entries: the register claims every base context is justified, which is not what " +
			"Phase 0 measured (F1.2 and F1.3 both found sites where a caller has a context and does not pass one)")
	}
	t.Logf("base contexts: root=%d detached=%d legacy=%d", rootCtx, detached, legacy)
}

// toFindings builds findings from baseline keys ("file|detail"), which is how a test states the register
// it wants without hand-writing the Finding struct at every case.
func toFindings(keys []string) []Finding {
	out := make([]Finding, 0, len(keys))
	for _, k := range keys {
		file, detail, _ := strings.Cut(k, "|")
		if rest := strings.TrimPrefix(k, file+"|"); rest != detail {
			detail = rest
		}
		out = append(out, Finding{File: file, Detail: detail})
	}
	return out
}

func mustBaseline(t *testing.T, path string) map[string]bool {
	t.Helper()
	m, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("load baseline %s: %v", path, err)
	}
	return m
}
