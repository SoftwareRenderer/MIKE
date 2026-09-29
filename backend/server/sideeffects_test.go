package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The classification is only a rule if something checks it against the route table. Every assertion here
// reads routes.go rather than a copy of it, so a route renamed out of the list is found (a stale entry is
// an unclassified path that still looks classified) and a new mutating route under a covered family is
// found too.

// reRegisteredRoute matches the verb-prefixed registrations routes.go writes.
var reRegisteredRoute = regexp.MustCompile(`s\.mux\.Handle(Func)?\("(GET|POST|PUT|PATCH|DELETE) (/api/[^"]*)`)

// coveredMutatingPrefixes are the families this file claims. A new mutating route inside one of them has to
// be classified before it can merge; outside them, classification is not yet claimed, and saying so is
// better than a test that implies more coverage than exists.
var coveredMutatingPrefixes = []string{
	"/api/projects/{projectID}/tasks/{taskID}/",
	"/api/projects/{projectID}/git/",
	"/api/extensions",
	"/api/fs/",
}

func registeredRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	var out []string
	for _, m := range reRegisteredRoute.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[2]+" "+m[3])
	}
	if len(out) < 50 {
		t.Fatalf("parsed %d verb-prefixed registrations from routes.go — the matcher is broken, and a "+
			"classification test that scans nothing passes by construction", len(out))
	}
	return out
}

func entries(sets ...[]sideEffectEntry) []sideEffectEntry {
	var out []sideEffectEntry
	for _, set := range sets {
		out = append(out, set...)
	}
	return out
}

func TestSideEffectClassificationIsDisjointAndExplained(t *testing.T) {
	seen := map[string]string{}
	for _, e := range entries(detachedOperations, requestScopedReads, idempotentWritesOnReads) {
		if e.Path == "" {
			t.Error("an entry with no path")
		}
		if strings.TrimSpace(e.Why) == "" {
			t.Errorf("%s: no reason — an entry nobody can justify is an entry the next reader deletes", e.Path)
		}
		if prior, dup := seen[e.Path]; dup {
			t.Errorf("%s is classified twice (%s and again) — one path, one answer", e.Path, prior)
		}
		seen[e.Path] = "classified"
	}

	// A path must not appear in two sets: "cancellable" and "not cancellable" on one route is the
	// contradiction the whole file exists to remove.
	for _, set := range [][]sideEffectEntry{detachedOperations, requestScopedReads, idempotentWritesOnReads} {
		for _, e := range set {
			hits := 0
			for _, other := range [][]sideEffectEntry{detachedOperations, requestScopedReads, idempotentWritesOnReads} {
				for _, o := range other {
					if o.Path == e.Path {
						hits++
					}
				}
			}
			if hits != 1 {
				t.Errorf("%s appears in %d sets, want exactly 1", e.Path, hits)
			}
		}
	}
}

func TestSideEffectClassificationNamesRoutesThatExist(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range registeredRoutes(t) {
		registered[r] = true
	}

	for _, e := range entries(detachedOperations, requestScopedReads, idempotentWritesOnReads) {
		if !strings.Contains(e.Path, " ") || !strings.HasPrefix(e.Path, "GET ") &&
			!strings.HasPrefix(e.Path, "POST ") && !strings.HasPrefix(e.Path, "PUT ") &&
			!strings.HasPrefix(e.Path, "PATCH ") && !strings.HasPrefix(e.Path, "DELETE ") {
			continue // a middleware entry, not a route
		}
		if !registered[e.Path] {
			t.Errorf("%s is classified but not registered — a renamed route keeps its classification and loses its meaning", e.Path)
		}
	}
}

// The coverage rule: inside the covered families, every mutating route is classified. A new one that skips
// this file is a route whose cancellation behaviour nobody decided.
func TestSideEffectClassificationCoversItsFamilies(t *testing.T) {
	classified := map[string]bool{}
	for _, e := range entries(detachedOperations, requestScopedReads, idempotentWritesOnReads) {
		classified[e.Path] = true
	}

	var missing []string
	for _, route := range registeredRoutes(t) {
		verb, path, _ := strings.Cut(route, " ")
		if verb == "GET" || verb == "HEAD" {
			continue
		}
		covered := false
		for _, prefix := range coveredMutatingPrefixes {
			if strings.HasPrefix(path, prefix) {
				covered = true
				break
			}
		}
		if !covered || classified[route] {
			continue
		}
		missing = append(missing, route)
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("mutating routes inside the covered families with no side-effect classification:\n  %s\n"+
			"Add each to detachedOperations (it leaves state behind) or requestScopedReads (it does not).",
			strings.Join(missing, "\n  "))
	}
}

// The one read that writes stays exactly one statement. This assertion is here so that adding a second
// write to that handler is a decision made twice — here and in the handler — rather than once.
func TestIdempotentWritesOnReadsIsStillJustTheSnapshot(t *testing.T) {
	if len(idempotentWritesOnReads) != 2 {
		t.Errorf("idempotentWritesOnReads has %d entries; the route set was %v",
			len(idempotentWritesOnReads), idempotentWritesOnReads)
	}
	for _, e := range idempotentWritesOnReads {
		if !strings.Contains(e.Why, "one") && !strings.Contains(e.Why, "One") && !strings.Contains(e.Why, "single") {
			t.Errorf("%s: an entry on a cancellable write path must name the single statement that makes it safe", e.Path)
		}
	}
}
