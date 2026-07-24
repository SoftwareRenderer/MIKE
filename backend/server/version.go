package server

// Version is set at build time via ldflags.
// Default value indicates an untagged/dev build.
//
//	go build -ldflags "-X kanban/backend/server.Version=v0.7.0" -o kanban .
var Version = "dev"
