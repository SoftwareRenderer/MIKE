package main

// sandboxprobe answers one question about this host: can it run a gVisor container?
//
// An installed runsc is not the answer, so this starts one throwaway container with the product's own create
// arguments and reports what came back. It touches nothing else and reads no MIKE_* variable; the image is a flag.
//
// With --build it asks the next question — can such a container build an image? — and runs the ladder in
// pkg/sandboxruntime, reporting every stage because a failed stage answers a different question than a passed one.

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

// buildVerdict is the ladder's answer plus every stage's, so --json keeps the detail the human
// output prints line by line.
type buildVerdict struct {
	Builds      bool                              `json:"builds"`
	RunsInGuest bool                              `json:"runs_in_guest"`
	Image       string                            `json:"image"`
	Storage     string                            `json:"storage"`
	Reason      string                            `json:"reason,omitempty"`
	Stages      []sandboxruntime.BuildProbeResult `json:"stages"`
}

func main() {
	image := flag.String("image", sandbox.BaseImage, "image to start the throwaway container from")
	builder := flag.String("builder", sandboxruntime.BuilderImageDefault, "with --build: the image that carries buildah")
	jsonOut := flag.Bool("json", false, "answer as JSON instead of one line")
	build := flag.Bool("build", false, "ask whether a gVisor container can build an image")
	flag.Parse()

	// One base context for whichever question is asked, each bounded by its own ceiling: the ladder
	// pulls a base image from inside the guest, so it needs more than a container start.
	timeout := sandboxruntime.ProbeTimeout
	if *build {
		timeout = sandboxruntime.BuildProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if *build {
		os.Exit(runBuildProbe(ctx, *builder, *jsonOut))
	}

	if err := sandboxruntime.ValidateImageRef(*image); err != nil {
		fmt.Fprintf(os.Stderr, "sandboxprobe: %v\n", err)
		os.Exit(2)
	}

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

// runBuildProbe prints one line per stage and returns the exit code: 0 only when a build step ran
// inside the guest, because that is the capability the ladder exists to measure.
func runBuildProbe(ctx context.Context, builder string, jsonOut bool) int {
	if err := sandboxruntime.ValidateImageRef(builder); err != nil {
		fmt.Fprintf(os.Stderr, "sandboxprobe: %v\n", err)
		return 2
	}

	rt := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{})

	// 	// Each stage removes its own container, but a deferred call cannot run if this command is killed. So this
	// 	// command sweeps its own family first and leaves running containers alone: a second probe must not stop
	// 	// another's.
	swept := rt.CleanupStaleContainers("build-probe", sandboxruntime.StaleBuildProbeFilter)
	for _, name := range swept {
		fmt.Printf("  removed leftover stage container %s\n", name)
	}

	rep, err := rt.ProbeBuild(ctx, builder)
	v := buildVerdict{
		Builds:      rep.Passed(),
		RunsInGuest: rep.RunsWithinGuest(),
		Image:       builder,
		Storage:     rep.Storage,
		Stages:      rep.Results,
	}
	if err != nil {
		v.Reason = err.Error()
	}

	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(v)
		return buildExitCode(v)
	}

	var informationalFailures []string
	for _, res := range v.Stages {
		label := ""
		if res.Informational {
			label = " (informational)"
			if !res.OK {
				informationalFailures = append(informationalFailures, res.Stage)
			}
		}
		if res.OK {
			fmt.Printf("  stage %-20s built an image%s\n", res.Stage, label)
			continue
		}
		fmt.Printf("  stage %-20s %s%s\n", res.Stage, res.Reason, label)
	}
	for _, stage := range informationalFailures {
		fmt.Printf("  note: stage %s measured a grant that does not build on this host.\n", stage)
	}
	if len(informationalFailures) > 0 {
		fmt.Println("        A larger grant is not a superset of what works: the wide set that omits CHOWN fails at")
		fmt.Println("        lchown while the same set with CHOWN, and the recommended defaults, build. That is why a")
		fmt.Println("        grant is tested whole — docs/adr/0002 holds the measurement and the table.")
	}
	switch {
	case v.RunsInGuest:
		fmt.Printf("build probe: a gVisor container built an image and ran a build step in it (%s, storage %s)\n", v.Image, v.Storage)
	case v.Builds:
		fmt.Printf("build probe: a gVisor container can build, but could not run a build step — %s\n", v.Reason)
	default:
		fmt.Printf("build probe: a gVisor container cannot build here — %s\n", v.Reason)
	}
	return buildExitCode(v)
}

func buildExitCode(v buildVerdict) int {
	if v.RunsInGuest {
		return 0
	}
	return 1
}
