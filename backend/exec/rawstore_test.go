package exec

import "kanban/backend/store"

// rawStore recovers the concrete store this executor was built with, for fixtures that
// need more than taskStore. Only a _test.go file can define it.
func (te *TaskExecutor) rawStore() *store.Store { return te.store.(*store.Store) }
