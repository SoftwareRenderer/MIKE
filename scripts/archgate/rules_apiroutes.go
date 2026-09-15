package main

// apiroutes reports a registered /api route that no caller puts on the wire — the gap `routes` leaves
// open, because that rule only asks whether a verb is declared. It is `wsevents` for the HTTP surface:
// a broadcast nobody listens to and a route nobody calls are the same debt, and the stream side has been
// measured since F9 while the route side never was. Five routes had already gone uncalled
// (`GET …/commit-preview`, `POST …/retry`, and three verb aliases on /api/extensions) before this rule
// existed, and no gate could have told anyone.
//
// A caller is a path literal in `frontend/src` or in the container healthcheck. `../MIKE-extensions` was
// checked and calls this server over no HTTP route at all: a UI extension reaches the host through
// `extHost`, and the capability handlers under `frontend/src/lib/extensions/handlers/` are what open the
// socket and make the proxy and storage calls. So the frontend plus the healthcheck is the whole consumer
// set, and a route outside both is unreachable by construction.
//
// Test files are not callers. A fixture URL in a unit test would excuse a dead route, which is the
// opposite of the question — the same reason `wsevents` strips the frontend's own union declaration
// before asking who reads an event.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// healthcheckFile is the second consumer: POSIX sh, so its URL is a literal in a `curl` line rather than
// a template string. /api/health's only caller in the tree is here.
const healthcheckFile = "scripts/mike-healthcheck.sh"

// apiRouteExemptions are routes whose URL no source spells out, each naming where it is built instead so
// the exemption can be re-checked rather than believed.
var apiRouteExemptions = map[string]string{
	// Both are opened as `new WebSocket(origin + API_BASE + "/stream")` and `wss://host/api/lsp`, from
	// api/stream.ts and utils/lsp-connection-manager.ts. They are already exempt from the `routes` rule
	// through wsUpgrades, for the same reason: they upgrade on GET and never inspect the verb.
	"GET /api/stream": "opened from window.location.origin + API_BASE in api/stream.ts",
	"GET /api/lsp":    "opened from location.host in utils/lsp-connection-manager.ts",
	// A {name...} tail can never be excused by a literal: the browser asks for it by resolving the
	// relative refs of the served document, and the server writes those refs —
	// rewriteRootRelative in backend/server/handler_preview.go, which rewrites src/href/action to
	// /api/preview/<token>/… at serve time. /api/preview/{token} and /nav are spelled in
	// extensions/handlers/preview.ts; only the sub-resource path is not.
	"GET /api/preview/{token}/{path...}": "the previewed document's own relative refs, rewritten server-side by rewriteRootRelative",
}

// pathTail is the run of characters a URL may contain inside a string literal. Every caller builds its URL
// out of a literal, so the tail ends at any of the three quotes — above all the backtick, because the
// common idiom is a template literal and a tail that ran past the closing quote collected the `,` and
// `)` of the surrounding code as part of the path. Whitespace and the query/fragment marks end it too, so
// `/x/build${opts?.no_cache…}` yields the path rather than the ternary. Braces and `$` end it because a
// `${…}` hole has already become varMark by the time a path is matched, so any brace left belongs to code.
const pathTail = `/[^` + "`" + `'"\s?#{}$]*`

var (
	// reAPIPath matches a path rooted at /api. `${API_BASE}` is rewritten to `/api` before matching, so
	// the constant idiom and a bare '/api/…' literal are one shape here.
	reAPIPath = regexp.MustCompile(`/api(?:` + pathTail + `)*`)
	// reRelAPIPath is a path-shaped literal in the one file that hands a path to a helper which prefixes
	// API_BASE itself (streamOperation's `path` argument in api/extensions.ts). It is consulted only in a
	// file that writes `${API_BASE}${`, and a match still has to line up with a registered pattern to
	// excuse anything.
	reRelAPIPath = regexp.MustCompile(`/(?:[a-z][a-z0-9_-]*(?:` + pathTail + `)*)`)
	// reSubst is one `${…}` hole. No URL hole nests a brace today; one that did would leave its inner
	// brace in the line, which ends the path there — reported rather than hidden.
	reSubst = regexp.MustCompile(`\$\{[^{}]*\}`)
	// reRouteVar is one Go mux variable: {id}, or {path...}, which absorbs the rest of the path.
	reRouteVar = regexp.MustCompile(`^\{([^{}]+)\}$`)
)

// apiBaseToken is the frontend's prefix as it appears in source; API_BASE is '/api' (api/helpers.ts), so
// rewriting the token to its value is what the browser puts on the wire.
const apiBaseToken = "${API_BASE}"

// varMark holds the place of a substitution while the path is split, so an empty segment is recognised as
// "a variable stood here" rather than dropped as a stray slash.
const varMark = "\x00"

// minClientPaths and minAPIRoutes are the undercount guards, vars so a fixture can pass them. A matcher
// that stopped matching would either report every route as uncalled (loud) or, if the caller set came back
// empty, nothing at all (silent) — the second is the one worth refusing. `wsevents` holds the same line
// for the same reason.
var (
	minClientPaths = 60
	minAPIRoutes   = 60
)

// normalisePath turns a caller's path or a route pattern into comparable segments. A `{mux}` variable and
// a whole-segment `${hole}` both become "*"; `{name...}` becomes "**". A hole that only *extends* a
// segment (`stream${syncParam}`, `build${opts?…}`) leaves the literal text, which is what the route's
// static segment must match — every such hole in this tree is a query string or a query-shaped ternary,
// not a path extension.
func normalisePath(p string) []string {
	p = strings.SplitN(p, "?", 2)[0]
	p = strings.SplitN(p, "#", 2)[0]
	p = reSubst.ReplaceAllString(p, varMark)

	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		if m := reRouteVar.FindStringSubmatch(seg); m != nil {
			if strings.HasSuffix(m[1], "...") {
				seg = "**"
			} else {
				seg = "*"
			}
		} else if strings.Contains(seg, varMark) {
			filled := strings.ReplaceAll(seg, varMark, "*")
			if filled == "*" {
				seg = "*" // the hole was the whole segment
			} else {
				seg = strings.TrimSuffix(filled, "*") // a suffix hole: keep the literal path
			}
		}
		out = append(out, seg)
	}
	return out
}

// callerPathsIn reads one caller's source and collects every path it can put on the wire. `indirect` says
// the file passes bare paths to a helper that prefixes API_BASE, which is the only way a path not rooted
// at /api reaches the server.
func callerPathsIn(text string, indirect bool, add func(raw string)) {
	for _, line := range strings.Split(strings.ReplaceAll(text, apiBaseToken, "/api"), "\n") {
		// A hole is a segment boundary before anything is matched: matching first would keep the
		// `${query ? ` of a templated query string in the tail, and the path would never line up with
		// the route it calls.
		line = reSubst.ReplaceAllString(line, varMark)
		for _, m := range reAPIPath.FindAllString(line, -1) {
			add(m)
		}
		if indirect {
			for _, m := range reRelAPIPath.FindAllString(line, -1) {
				add(m)
			}
		}
	}
}

// clientPaths collects every path the callers can put on the wire, normalised as a route pattern is so
// the comparison is segment-for-segment.
func clientPaths(root string) (map[string]bool, error) {
	out := map[string]bool{}
	add := func(raw string) {
		p := strings.TrimSpace(raw)
		if !strings.HasPrefix(p, "/api") {
			p = "/api" + p
		}
		if segs := normalisePath(p); len(segs) > 0 {
			out[strings.Join(segs, "/")] = true
		}
	}

	dir := filepath.Join(root, "frontend", "src")
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("cannot read frontend/src")
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || skipDirs[info.Name()] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".ts") && !strings.HasSuffix(rel, ".svelte") {
			return nil
		}
		// A test is not a caller — see the file comment.
		if strings.Contains(rel, "/__tests__/") || strings.Contains(rel, "/tests/") ||
			strings.Contains(info.Name(), ".unit.") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(raw)
		callerPathsIn(text, strings.Contains(text, apiBaseToken+"${"), add)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, line := range read(root, healthcheckFile) {
		for _, m := range reAPIPath.FindAllString(reSubst.ReplaceAllString(line, varMark), -1) {
			add(m)
		}
	}

	if len(out) < minClientPaths {
		return nil, fmt.Errorf("only %d caller paths found — the matcher is wrong, not the tree", len(out))
	}
	return out, nil
}

// scanAPIRoutes reports every /api route with a declared verb that no caller reaches.
func scanAPIRoutes(root string) ([]Finding, int, error) {
	routes, err := loadRoutes(root)
	if err != nil {
		return nil, 0, err
	}
	callees, err := clientPaths(root)
	if err != nil {
		return nil, 0, err
	}

	var apiRoutes []route
	for _, r := range routes {
		if strings.HasPrefix(r.pattern, "/api") && r.method != "" {
			apiRoutes = append(apiRoutes, r)
		}
	}
	if len(apiRoutes) < minAPIRoutes {
		return nil, 0, fmt.Errorf("only %d /api routes with a declared verb parsed — the matcher is wrong, not the tree", len(apiRoutes))
	}

	var out []Finding
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, exempt := apiRouteExemptions[key]; exempt {
			continue
		}
		segs := normalisePath(r.pattern)
		if callees[strings.Join(segs, "/")] {
			continue
		}
		out = append(out, Finding{
			File: routesFile, Line: r.line,
			Detail: "route " + key + " has no caller",
			Why: "no path literal in frontend/src or " + healthcheckFile + " reaches it, so no client can ask " +
				"for this handler. Either delete the route, or add it to apiRouteExemptions naming where its " +
				"URL is built — a route nobody calls still costs a verb, a CSRF arm, and a classification",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Detail < out[j].Detail })
	return out, len(apiRoutes), nil
}
