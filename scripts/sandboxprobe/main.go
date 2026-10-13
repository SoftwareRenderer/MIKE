package main

// sandboxprobe answers one question about this host: can it run a gVisor container?
//
// The cheap answer — is runsc installed — is not it, so this starts one throwaway container through
// the product's own create arguments and reports what came back. It changes nothing outside the one
// container it creates and removes, and it reads no MIKE_* variable: the image is a flag.
//
// Exit codes: 0 usable, 1 not usable, 2 the input was refused.
//
// Usage:
//
//	go run ./scripts/sandboxprobe                 # human-readable, one line
//	go run ./scripts/sandboxprobe --json          # {"usable":…,"reason":…} for the e2e harness
//	go run ./scripts/sandboxprobe --image localhost/mike-test:1

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mike/backend/sandbox"
	"mike/pkg/sandboxruntime"
)

type verdict struct {
	Usable bool   `json:"usable"`
	Image  string `json:"image"`
	Reason string `json:"reason,omitempty"`
}

func main() {
	image := flag.String("image", sandbox.BaseImage, "image to start the throwaway container from")
	jsonOut := flag.Bool("json", false, "answer as JSON instead of one line")
	flag.Parse()

	if err := sandboxruntime.ValidateImageRef(*image); err != nil {
		fmt.Fprintf(os.Stderr, "sandboxprobe: %v\n", err)
		os.Exit(2)
	}

	// Bounded, so a wedged podman costs this command ProbeTimeout and no more.
	ctx, cancel := context.WithTimeout(context.Background(), sandboxruntime.ProbeTimeout)
	defer cancel()

	err := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}).ProbeSandbox(ctx, *image)
	v := verdict{Usable: err == nil, Image: *image}
	if err != nil {
		v.Reason = err.Error()
	}

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(v)
	} else if v.Usable {
		fmt.Printf("sandbox: usable (probed %s)\n", v.Image)
	} else {
		fmt.Printf("sandbox: NOT SUPPORTED in this environment — %s\n", v.Reason)
	}
	if !v.Usable {
		os.Exit(1)
	}
}
