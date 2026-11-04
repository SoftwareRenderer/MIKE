package server

import (
	"strings"
	"testing"
)

// A path sits in exactly one of the three sets, and says why. A path in two sets would be cancellable and
// not cancellable at once; an entry with no reason is one the next reader deletes.
func TestSideEffectClassificationIsDisjointAndExplained(t *testing.T) {
	sets := map[string][]sideEffectEntry{
		"detachedOperations":      detachedOperations,
		"requestScopedReads":      requestScopedReads,
		"idempotentWritesOnReads": idempotentWritesOnReads,
	}

	seen := map[string]string{}
	for name, set := range sets {
		for _, e := range set {
			if e.Path == "" {
				t.Errorf("%s: an entry with no path", name)
			}
			if strings.TrimSpace(e.Why) == "" {
				t.Errorf("%s: no reason — an entry nobody can justify is an entry the next reader deletes", e.Path)
			}
			if prior, dup := seen[e.Path]; dup {
				t.Errorf("%s classified in %s and in %s — one path, one answer", e.Path, prior, name)
			}
			seen[e.Path] = name
		}
	}
}
