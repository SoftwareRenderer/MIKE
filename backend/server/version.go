package server

// Version is set at build time via ldflags.
// Default value indicates an untagged/dev build.
//
//	go build -ldflags "-X mike/backend/server.Version=v0.7.0" -o mike .
var Version = "dev"
