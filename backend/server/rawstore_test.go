package server

import "kanban/backend/store"

// rawStore recovers the concrete store this server was built with, for fixtures that seed
// rows the API has no route for. The assertion fails loudly if a test ever runs against a
// fake.
func (s *Server) rawStore() *store.Store { return s.store.(*store.Store) }
