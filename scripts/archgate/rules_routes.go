package main

// Route rules. Both resolve a handler's receiver type from the Server struct field rather than from
// the method name: `HandleList` is defined on ProjectHandler, TaskHandler and ExtensionHandler, and a
// name-matching first pass reported a method-aware handler as method-agnostic. A route tool that gets
// the handler wrong prints confident nonsense.

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// routesFile and serverFile are the two inputs; both are named so a moved file is a loud scan
// failure rather than an empty finding list.
const (
	routesFile = "backend/server/routes.go"
	serverFile = "backend/server/server.go"
)

// wsUpgrades are exempt from the method rule: a WebSocket route upgrades on GET and its handler
// never inspects the method, so prefixing it buys nothing and hides the exemption.
var wsUpgrades = map[string]bool{
	"/api/stream": true,
	"/api/lsp":    true,
}

type route struct {
	pattern string
	method  string // "" when registered without a method prefix
	field   string // the Server struct field, e.g. "gitHandler"
	handler string // the method name, e.g. "HandleCheckout"
	line    int
	opaque  bool // registered with a handler expression, so there is no body to inspect
	inline  bool // the handler is a closure written in routes.go itself
}

var (
	// mux.Handle with a handler expression: the file servers and the rate-limited login route.
	rePlainHandle = regexp.MustCompile(`\.Handle\(\s*"([^"]+)"\s*,`)
	// The pattern is captured first and the handler classified after: three shapes register a route
	// (a method on an injected manager, a method on *Server, a closure written inline), and a matcher
	// that assumes the handler starts with "s." silently drops the six inline ones — which are
	// /api/extensions/{id} and its friends, not a corner nobody uses.
	reHandleFunc = regexp.MustCompile(`\.HandleFunc\(\s*"([^"]+)"\s*,\s*(.*)$`)
	reStructLine = regexp.MustCompile(`^\s*(\w+)\s+\*?([\w.]+)\s*(//.*)?$`)
	reMethodHTTP = regexp.MustCompile(`\br\.Method\b`)
)

// loadRoutes reads the route table.
func loadRoutes(root string) ([]route, error) {
	lines := read(root, routesFile)
	if lines == nil {
		return nil, fmt.Errorf("cannot read %s", routesFile)
	}
	var out []route
	for i, line := range lines {
		if m := rePlainHandle.FindStringSubmatch(line); m != nil {
			pattern := m[1]
			r := route{pattern: pattern, line: i + 1, opaque: true}
			if parts := strings.SplitN(pattern, " ", 2); len(parts) == 2 {
				r.method, r.pattern = parts[0], parts[1]
			}
			out = append(out, r)
			continue
		}
		m := reHandleFunc.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pattern, rest := m[1], strings.TrimSpace(m[2])
		r := route{pattern: pattern, line: i + 1}
		switch {
		case strings.HasPrefix(rest, "func("):
			r.inline = true
		case strings.Count(rest, ".") >= 2: // s.gitHandler.HandleCheckout
			after := strings.TrimPrefix(rest, "s.")
			if dot := strings.Index(after, "."); dot > 0 {
				r.field, r.handler = after[:dot], strings.TrimSuffix(after[dot+1:], ")")
			}
		case strings.HasPrefix(rest, "s."): // s.handleAuthSession
			r.handler = strings.TrimSuffix(strings.TrimPrefix(rest, "s."), ")")
		}
		if parts := strings.SplitN(pattern, " ", 2); len(parts) == 2 {
			r.method, r.pattern = parts[0], parts[1]
		}
		out = append(out, r)
	}
	// Self-check, not decoration: a route tool that silently parses fewer routes than the file
	// registers reports a smaller debt than exists, which is the failure this whole gate is aimed at.
	registered := 0
	for _, line := range lines {
		if strings.Contains(line, "HandleFunc(") || strings.Contains(line, "mux.Handle(") {
			registered++
		}
	}
	if len(out) < registered {
		return nil, fmt.Errorf("%s: parsed %d routes but %d registration lines exist — the matcher is blind to %d of them",
			routesFile, len(out), registered, registered-len(out))
	}
	return out, nil
}

func shortType(typ string) string {
	typ = strings.TrimPrefix(typ, "*")
	if i := strings.LastIndex(typ, "."); i >= 0 {
		typ = typ[i+1:]
	}
	return typ
}

// inlineStructBody returns the field list of a one-line struct declaration.
func inlineStructBody(line string) (string, bool) {
	i := strings.Index(line, "type Server struct{")
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSuffix(strings.TrimSpace(line[i+len("type Server struct{"):]), "}")
	return rest, true
}

// serverFieldTypes maps each Server struct field to its type name, which is how a handler is bound
// to the right receiver. Values are like "*api.GitHandler"; the type name is the last segment.
func serverFieldTypes(root string) (map[string]string, error) {
	lines := read(root, serverFile)
	if lines == nil {
		return nil, fmt.Errorf("cannot read %s", serverFile)
	}
	out := map[string]string{}
	inStruct := false
	for _, line := range lines {
		// `type Server struct{ mux any }` on one line is legal Go and was invisible here: the parser
		// matched only a struct that opens at end of line, so it found zero fields and every rule that
		// resolves a handler through the Server struct failed — loudly in this direction, which is the
		// only reason it was noticed rather than silently resolving nothing.
		if inline, ok := inlineStructBody(line); ok {
			for _, field := range strings.Split(inline, ";") {
				if m := reStructLine.FindStringSubmatch(strings.TrimSpace(field)); m != nil && m[1] != "" {
					out[m[1]] = shortType(m[2])
				}
			}
			continue
		}
		if strings.HasPrefix(line, "type Server struct {") {
			inStruct = true
			continue
		}
		if inStruct && line == "}" {
			break
		}
		if !inStruct {
			continue
		}
		m := reStructLine.FindStringSubmatch(line)
		if m == nil || m[1] == "" {
			continue
		}
		out[m[1]] = shortType(m[2])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no Server struct fields parsed", serverFile)
	}
	return out, nil
}

// goFilesByType maps a type name to **every** file that declares it or implements on it.
//
// The plural is the whole point. A type's methods are spread across the package - *Server alone has
// methods in twenty files - so mapping a type to its declaration file makes methodBody find nothing
// for most handlers, and a rule that finds nothing reports a clean tree. The first version of this
// function did exactly that and undercounted the routes baseline.
func goFilesByType(root string) (map[string][]string, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	// `struct {` and `struct{ x int }` are both gofmt output; a pattern requiring the space silently
	// resolves no types, which makes every route rule report a clean tree.
	reDecl := regexp.MustCompile(`^type (\w+) struct\s*\{`)
	reRecv := regexp.MustCompile(`^func \(\w+ \*?(\w+)\) `)
	for _, rel := range files {
		for _, line := range read(root, rel) {
			name := ""
			if m := reDecl.FindStringSubmatch(line); m != nil {
				name = m[1]
			} else if m := reRecv.FindStringSubmatch(line); m != nil {
				name = m[1]
			}
			if name == "" {
				continue
			}
			list := out[name]
			if len(list) == 0 || list[len(list)-1] != rel {
				out[name] = append(list, rel)
			}
		}
	}
	return out, nil
}

// methodBody returns the source of a method, searching every file associated with its receiver type.
// Brace counting is enough here because Go gofmts its braces.
func methodBody(root string, files []string, recvType, name string) string {
	reDecl := regexp.MustCompile(`^func \(\w+ \*?` + regexp.QuoteMeta(recvType) + `\) ` + regexp.QuoteMeta(name) + `\(`)
	for _, file := range files {
		lines := read(root, file)
		for i, line := range lines {
			if !reDecl.MatchString(line) {
				continue
			}
			depth := 0
			var b strings.Builder
			for j := i; j < len(lines); j++ {
				b.WriteString(lines[j])
				b.WriteString("\n")
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if depth == 0 && j > i {
					return b.String()
				}
			}
		}
	}
	return ""
}

// scanRoutes reports every route registered without a method, and every handler that answers a
// methodless route by switching on r.Method itself.
func scanRoutes(root string) ([]Finding, int, error) {
	routes, err := loadRoutes(root)
	if err != nil {
		return nil, 0, err
	}
	types, err := serverFieldTypes(root)
	if err != nil {
		return nil, 0, err
	}
	byType, err := goFilesByType(root)
	if err != nil {
		return nil, 0, err
	}

	var out []Finding
	for _, r := range routes {
		if r.method != "" || wsUpgrades[r.pattern] {
			continue
		}
		out = append(out, Finding{
			File: routesFile, Line: r.line, Detail: "route " + r.pattern + " accepts any method",
			Why: "prefix the pattern with its method; a write registered methodless escapes the CSRF middleware, which guards non-GET only (F17)",
		})

		body := routeBody(root, types, byType, r)
		if body != "" && reMethodHTTP.MatchString(body) {
			out = append(out, Finding{
				File: routesFile, Line: r.line, Detail: "route " + r.pattern + " handler " + handlerLabel(types, r) + " dispatches on r.Method",
				Why: "move the switch into per-method routes; today an unhandled method falls through silently (F6)",
			})
		}
	}
	return out, len(routes), nil
}

// routeBody returns the handler source for any of the three registration shapes: a closure written
// in routes.go, a method on an injected manager, or a method on *Server. The inline closure is not an
// edge case to skip — six /api/extensions routes are closures, including the two POST-only writes.
func routeBody(root string, types map[string]string, byType map[string][]string, r route) string {
	if r.inline {
		return inlineBody(root, routesFile, r.line)
	}
	if r.opaque {
		return ""
	}
	recv := receiverOf(types, r)
	return methodBody(root, byType[recv], recv, r.handler)
}

// inlineBody brace-counts from a registration line to the end of the closure that starts on it.
func inlineBody(root, file string, line int) string {
	lines := read(root, file)
	if line < 1 || line > len(lines) {
		return ""
	}
	depth := 0
	started := false
	var b strings.Builder
	for j := line - 1; j < len(lines); j++ {
		b.WriteString(lines[j])
		b.WriteString("\n")
		for _, ch := range lines[j] {
			switch ch {
			case '{':
				depth++
				started = true
			case '}':
				depth--
			}
		}
		if started && depth <= 0 {
			return b.String()
		}
	}
	return b.String()
}

// handlerLabel names a handler in a finding message, including the closure shape.
func handlerLabel(types map[string]string, r route) string {
	if r.inline {
		return "routes.go:closure"
	}
	return receiverOf(types, r) + "." + r.handler
}

// receiverOf resolves the type a route's handler is defined on. An empty field means the handler
// hangs off *Server itself.
func receiverOf(types map[string]string, r route) string {
	if r.field == "" {
		return "Server"
	}
	return types[r.field]
}

// storeMutation is the shape of a state write: a call on a *store.Store through any of the names
// handlers use. Kept narrow on purpose — a broad pattern made F19 report twelve writable GETs and
// eleven of them were the handler's POST branch.
var storeMutation = regexp.MustCompile(`\b\w*[sS]tore\.(Create|Update|Delete|Set|Upsert|Clear|Pin|Unpin|Remap|Accumulate|Transition|Backfill|Copy|Drop|Restore)\w*\(`)

// scanCancellableWrites reports a GET route whose handler writes state. A cancelled request must leave
// the server consistent, so a write on a cancellable path is only safe as one idempotent statement —
// `UpsertTaskDiffSnapshot`, the single baselined case, is a lone INSERT … ON CONFLICT DO UPDATE. A
// second write on such a handler can leave a partial body cached.
//
// writeNamedCall is the second signal: a package-level helper that persists, named for what it does.
// `stream.MaterializeDoneTaskDiff` is that shape — the one GET-reachable write in the tree, and no
// `store.` pattern reaches it, so a rule looking only for store method calls reports a clean tree
// over a real writable GET. The verb leads the identifier, so the pattern matches a lower-case verb.
var writeNamedCall = regexp.MustCompile(`\b(Materialize|Persist|Upsert|Save|Record|Append)\w*\(`)

// cacheWrite is the third signal: an in-memory memo populated on a read path. It reads the field's name, not
// its type, so a memo field stays in this rule's sight by being named for what it is.
var cacheWrite = regexp.MustCompile(`\.\w*[Cc]ache\.(Store|put)\(`)

func scanCancellableWrites(root string) ([]Finding, int, error) {
	routes, err := loadRoutes(root)
	if err != nil {
		return nil, 0, err
	}
	types, err := serverFieldTypes(root)
	if err != nil {
		return nil, 0, err
	}
	byType, err := goFilesByType(root)
	if err != nil {
		return nil, 0, err
	}

	var out []Finding
	// Which verbs can reach a given registration expression. A handler registered for GET+POST+DELETE
	// dispatches inside, so only its GET branch is code a GET runs.
	verbs := map[string]map[string]bool{}
	for _, r := range routes {
		// Keyed by receiver+name: `HandleList` exists on ProjectHandler, TaskHandler and
		// ExtensionHandler, and keying by bare name gives one route the verbs another package's
		// same-named handler answers.
		key := routeKey(r, types)
		if verbs[key] == nil {
			verbs[key] = map[string]bool{}
		}
		verbs[key][r.method] = true
	}

	for _, r := range routes {
		if r.method != "GET" {
			continue
		}
		body := routeBody(root, types, byType, r)
		if body == "" {
			continue
		}
		methods := verbs[routeKey(r, types)]
		if methods == nil {
			methods = map[string]bool{r.method: true}
		}
		single := len(methods) <= 1
		get, branchOK := getBranch(body, single)
		if !branchOK {
			out = append(out, Finding{
				File: orDefault(firstOf(byType[receiverOf(types, r)]), routesFile),
				Detail: "GET " + r.pattern + " serves " + strings.Join(slices.Sorted(maps.Keys(methods)), ",") +
					" with no identifiable GET branch",
				Why: "the rule cannot say which path a GET takes, so it cannot say the path is write-free — read it by hand",
			})
			continue
		}
		hits := map[string]bool{}
		for _, pat := range []*regexp.Regexp{storeMutation, writeNamedCall, cacheWrite} {
			for _, m := range pat.FindAllString(get, -1) {
				hits[m] = true
			}
		}
		for _, call := range slices.Sorted(maps.Keys(hits)) {
			out = append(out, Finding{
				File: findingFile(r, types, byType), Detail: "GET " + r.pattern + " writes: " + call,
				Why: "a write on a cancellable GET must be a single idempotent statement, or a cancelled request can persist a partial result (F19)",
			})
		}
	}
	return out, len(routes), nil
}

// getBranch narrows a handler body to the code a GET request actually runs.
//
// Without it, a handler registered for GET+POST+DELETE reports the writes in its POST branch as GET
// writes — 22 false accusations on the first run after the routes gained method prefixes, which is
// the kind of noise that gets a rule ignored. Three shapes:
//
//   - `switch r.Method { case http.MethodGet: … }` → that case, up to the next case/default/brace
//   - `if r.Method == http.MethodGet { … }`        → that block, by brace matching
//   - no dispatch                                  → the whole body, valid only when GET is the sole
//     registered verb
//
// ok=false means a multi-verb handler with no identifiable GET branch. That is reported as its own
// finding: "cannot tell" is not "clean", and the F17 lesson is that a silent gap reads as a pass.
// routeKey identifies the code a request reaches: the resolved handler including its receiver type,
// or for an inline closure the path, since a closure has no name to be confused with.
func routeKey(r route, types map[string]string) string {
	// An inline closure was written for the registration it sits in, so its verb set is that one
	// registration. Keying by path alone gave the four /api/extensions/{id} closures — GET, PUT,
	// PATCH, DELETE, each a separate body — one shared four-verb set, and the rule then demanded a
	// GET branch from a body that only ever answers GET.
	if r.inline {
		return "inline|" + r.pattern + "|" + r.method
	}
	if r.opaque {
		return "opaque|" + r.pattern
	}
	if t := types[r.field]; t != "" {
		return t + "." + r.handler
	}
	return r.field + "." + r.handler
}

// afterMethodGuard returns the body following `if r.Method != http.MethodGet { … return }`.
func afterMethodGuard(body, verb string) (string, bool) {
	needle := "r.Method != " + methodConst(verb)
	i := strings.Index(body, needle)
	if i < 0 {
		return "", false
	}
	brace := strings.Index(body[i:], "{")
	if brace < 0 {
		return "", false
	}
	depth, end := 0, -1
	for j := i + brace; j < len(body); j++ {
		switch body[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = j
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", false
	}
	return body[end+1:], true
}

// findingFile names the file a finding belongs to. A closure lives in routes.go, and a Server method
// lives in one of many files, so `first file of the type` would blame whichever happens to sort first
// — auth_handler.go answered for extensions routes until it was fixed.
func findingFile(r route, types map[string]string, byType map[string][]string) string {
	if r.inline || r.opaque {
		return routesFile
	}
	if files := byType[types[r.field]]; len(files) > 0 {
		return files[0]
	}
	return routesFile
}

func getBranch(body string, singleVerb bool) (string, bool) {
	if caseBody, found := extractMethodCase(body, "GET"); found {
		return caseBody, true
	}
	if block, found := extractMethodIf(body, "GET"); found {
		return block, true
	}
	// The dispatcher shape: `if r.Method == Delete { … }` and `== Post { … }` early-return above, and
	// `if r.Method != Get { refuse; return }` closes the door behind them. Everything after that guard
	// is code only a GET runs. handleTaskMessages is written this way, and without this case the rule
	// would report "cannot tell" about a handler that is perfectly legible.
	if rest, found := afterMethodGuard(body, "GET"); found {
		return rest, true
	}
	if singleVerb {
		return body, true
	}
	return "", false
}

// methodConst maps a wire verb to the net/http constant a handler compares against. Written out
// rather than through strings.Title, which is deprecated.
func methodConst(verb string) string {
	if verb == "" {
		return "http.Method"
	}
	return "http.Method" + strings.ToUpper(verb[:1]) + strings.ToLower(verb[1:])
}

// extractMethodCase returns the body of the `case http.MethodGet:` arm, including a grouped
// `case http.MethodGet, http.MethodHead:` arm.
func extractMethodCase(body, verb string) (string, bool) {
	si := strings.Index(body, "switch r.Method {")
	if si < 0 {
		return "", false
	}
	lines := strings.Split(body[si:], "\n")
	for idx, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "case ") || !strings.Contains(t, methodConst(verb)) {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		var out []string
		for _, l := range lines[idx+1:] {
			lt := strings.TrimSpace(l)
			li := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			if li == indent && (lt == "}" || strings.HasPrefix(lt, "case ") || strings.HasPrefix(lt, "default:")) {
				break
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n"), true
	}
	return "", false
}

// extractMethodIf returns the balanced block of `if r.Method == http.MethodGet {`.
func extractMethodIf(body, verb string) (string, bool) {
	i := strings.Index(body, "r.Method == "+methodConst(verb))
	if i < 0 {
		return "", false
	}
	brace := strings.Index(body[i:], "{")
	if brace < 0 {
		return "", false
	}
	depth, end := 0, -1
	for j := i + brace; j < len(body); j++ {
		switch body[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = j
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", false
	}
	return body[i+brace : end+1], true
}

// scanWSEvents reports a Go app-stream event that no frontend code names. The reverse direction
// lands with the TypeScript union in Phase 4: the browser currently compares `.type` against 43
// literals, 23 of which belong to the terminal WebSocket, the extension channel or plain JSON, so
// "TS names an event Go does not define" is not yet a question with a clean answer (F9).
func scanWSEvents(root string) ([]Finding, int, error) {
	events, err := goEvents(root)
	if err != nil {
		return nil, 0, err
	}
	frontendNames, err := frontendTypeStrings(root)
	if err != nil {
		return nil, 0, err
	}
	union, err := tsEventUnion(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	for name, where := range events {
		if !frontendNames[name] {
			out = append(out, Finding{
				File: where.file, Line: where.line, Detail: "event " + name + " is never read by the frontend",
				Why: "a broadcast nobody listens to is either dead code or a UI panel that silently stopped updating",
			})
		}
		if _, inUnion := union[name]; !inUnion {
			out = append(out, Finding{
				File: where.file, Line: where.line, Detail: "event " + name + " is missing from the frontend's AppStreamEventType union",
				Why: "the union is the browser's declared list of what it dispatches; an event absent from it is " +
					"a frame the dispatcher's type does not admit, so a comparison against it stops compiling",
			})
		}
	}
	for name, where := range union {
		if _, defined := events[name]; !defined {
			out = append(out, Finding{
				File: where.file, Line: where.line, Detail: "the frontend's union declares " + name + ", which no Go constant defines",
				Why: "the dispatcher branch for it can never run, and the frame that would have carried it is " +
					"rejected by the type the union gives the parsed message",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Detail < out[j].Detail })
	return out, len(events) + len(union), nil
}

// appStreamUnion is the file where the browser declares the frame names it dispatches. It is a declaration
// rather than a grep over 43 `.type === 'x'` literals because those literals belong to three sockets, the
// clone stream's SSE events and the offline queue's action names as well, and a reverse check against that
// pile reports half the tree as inventing events.
const appStreamUnion = "frontend/src/lib/api/events.ts"

// streamEventUnion is the browser's second vocabulary declaration, for the per-task events that ride
// inside a task_event frame. Same trap as appStreamUnion: written down is not read.
const streamEventUnion = "frontend/src/lib/types/index.ts"

// unionNameByFile is the file-to-union map of those declarations. The block is stripped rather than the
// file, because lib/types/index.ts also holds real code and its readers still count.
var unionNameByFile = map[string]string{
	appStreamUnion:   "AppStreamEventType",
	streamEventUnion: "StreamEventType",
}

// stripUnion removes `export type <name> = 'a' | 'b' | … ;`. A union in a shape this does not recognize
// stays in the text, so an unrecognized name is reported as unread rather than excused.
func stripUnion(src, name string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	dropping := false
	for _, line := range lines {
		if !dropping && strings.Contains(line, "export type "+name) {
			dropping = true
		}
		if dropping {
			if strings.Contains(line, ";") {
				dropping = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// tsEventUnion reads the members of `export type AppStreamEventType = 'a' | 'b' | …`.
func tsEventUnion(root string) (map[string]loc, error) {
	raw, err := os.ReadFile(filepath.Join(root, appStreamUnion))
	if err != nil {
		return nil, err
	}
	out := map[string]loc{}
	member := regexp.MustCompile(`'([a-z][a-z_]*)'`)
	declared := false
	for i, line := range strings.Split(string(raw), "\n") {
		if !declared {
			declared = strings.Contains(line, "export type AppStreamEventType")
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "export") || strings.HasPrefix(strings.TrimSpace(line), "}") {
			break
		}
		for _, m := range member.FindAllStringSubmatch(line, -1) {
			out[m[1]] = loc{file: appStreamUnion, line: i + 1}
		}
		if strings.Contains(line, ";") {
			break
		}
	}
	// The real union is two dozen names; a handful means the declaration shape changed and the pattern
	// stopped matching, which must not be reported as a clean contract.
	if len(out) < 10 {
		return nil, fmt.Errorf("only %d union members found in %s — the matcher is wrong, not the tree", len(out), appStreamUnion)
	}
	return out, nil
}

type loc struct {
	file string
	line int
}

// goEvents collects the app-stream vocabulary: the WSType* constants and the string values they
// carry. That is the authoritative list the frontend mirrors.
func goEvents(root string) (map[string]loc, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	// The app-stream vocabulary only, from the package that owns it. F9's lesson: the agent adapter
	// protocol (pkg/agent EventType*) and the terminal WebSocket each have their own vocabulary and
	// their own consumers, and sweeping them in reports protocol events as unlistened-to broadcasts.
	// `WSTypeToken WSMessageType = "token"` — the named type sits between the identifier and the
	// value, so a pattern that expects `=` next to the name finds nothing and reports a clean tree.
	reConst := regexp.MustCompile(`WSType\w+\s+\w+\s*=\s*"([^"]+)"`)
	out := map[string]loc{}
	found := 0
	for _, rel := range files {
		if !strings.HasPrefix(rel, "backend/types/") {
			continue
		}
		for i, line := range read(root, rel) {
			m := reConst.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			out[m[1]] = loc{file: rel, line: i + 1}
			found++
		}
	}
	if found < 20 {
		return nil, fmt.Errorf("only %d WSType* constants found in backend/types — the matcher is wrong, not the tree", found)
	}
	return out, nil
}

// frontendTypeStrings reads every frontend source file for quoted strings, which is the superset a
// Go event name must appear in to count as "read by the frontend". Deliberately a superset: this
// rule asks only whether a broadcast has any listener at all.
func frontendTypeStrings(root string) (map[string]bool, error) {
	dir := filepath.Join(root, "frontend", "src")
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("cannot read frontend/src")
	}
	out := map[string]bool{}
	reStr := regexp.MustCompile(`["']([a-z][a-z0-9_]{2,})["']`)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if skipDirs[info.Name()] || (!strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".svelte")) {
			return nil
		}
		// A union declaration is not a consumer. Counting it as a reader would let the frontend silence
		// "nobody reads this broadcast" by writing the name down, which is the opposite of what the list is
		// for — and the app stream has two such declarations, not one.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if unionName, declared := unionNameByFile[filepath.ToSlash(rel)]; declared {
			raw = []byte(stripUnion(string(raw), unionName))
		}
		for _, m := range reStr.FindAllStringSubmatch(string(raw), -1) {
			out[m[1]] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("frontend/src yielded no strings — the matcher is wrong")
	}
	return out, nil
}

// firstOf returns the first file of a type's file list, for naming a finding.
func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
