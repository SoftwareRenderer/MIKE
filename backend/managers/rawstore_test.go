package managers

import "kanban/backend/store"

// rawStore recovers the concrete store this handler was built with, for fixtures that seed
// rows its narrow store interface cannot reach. It is defined on the one handler that needs
// it, in a _test.go file, so production cannot do the same.
func (h *ProjectHandler) rawStore() *store.Store { return h.store.(*store.Store) }
