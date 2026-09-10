package main

// wsframe seals the app-stream event vocabulary to one spelling: the WSType* constants in
// backend/types/ws.go. `wsevents` can compare only a name it can find, so an event named by a string
// literal is invisible to the parity check — which is how 13 event names, `task_event` among them,
// were broadcast while that rule printed "0 violations across 24 checked".
//
// Two scans. Inside a frame home (frameHomes), every inline `"type": "<name>"` is reported; anywhere
// in the tree, every `types.StreamEvent{Type: "<name>"}` is, because that shape names an app-stream
// event without building a frame there. The fix in both cases is a WSType constant, which puts the
// name in front of the parity rule from then on.
//
// Two vocabularies stay outside this rule because they are not the app stream: the clone stream's SSE
// names (`status`, `progress`, `done`, `error`) and the terminal WebSocket's `ping`. They collide with
// app-stream names by accident, and a rule that reports a working protocol as a violation gets
// ignored rather than fixed.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reFrameType matches a frame's event name written inline: `"type": "board_refresh"`.
var reFrameType = regexp.MustCompile(`"type":\s*"([^"]+)"`)

// reAppFrame counts the sealed way a frame is named: appFrame(types.WSType…, …). It exists so the rule's
// population is "frames built", which stays true after the literals are gone — a rule whose population is
// "literals found" reports a fixed tree as a broken matcher.
var reAppFrame = regexp.MustCompile(`\bappFrame\(types\.WSType\w+`)

// reAppFrameLiteral catches the shortcut that would undo the sealing from inside the builder: a string
// passed where the typed name belongs.
var reAppFrameLiteral = regexp.MustCompile(`\bappFrame\("`)

// reStreamEventLiteral catches the one shape that names an app-stream event from outside a frame home:
// types.StreamEvent{Type: "summarization"}. Two of these lived in backend/exec, goEvents reads only
// WSType* constants, and the frame scan looked only in the frame homes — so no rule saw them, and the
// parity check was comparing a name nothing had confirmed was still emitted.
var reStreamEventLiteral = regexp.MustCompile(`types\.StreamEvent\{Type:\s*"([^"]+)"`)

// reStreamEventConst counts the same shape when a constant names it, so the population stays "events
// named" once the literals are gone.
var reStreamEventConst = regexp.MustCompile(`types\.StreamEvent\{Type:\s*types\.WSType\w+`)

// frameHomes are the packages that build frames, each with the vocabulary its names belong to. A frame
// built anywhere else is either a protocol this rule has not been told about or is about to become one.
var frameHomes = map[string]string{
	"backend/stream/":   "WSType",  // the app-stream WebSocket, read by api/stream.ts
	"backend/managers/": "SSEType", // the clone stream, read by api/projects.ts
}

// reSSEConst collects the clone stream's vocabulary, which lives beside the WebSocket's but is not it.
var reSSEConst = regexp.MustCompile(`SSEType\w+\s+\w+\s*=\s*"([^"]+)"`)

func scanWSFrameLiterals(root string) ([]Finding, int, error) {
	vocab, err := goEvents(root)
	if err != nil {
		return nil, 0, err
	}
	sse, err := sseEvents(root)
	if err != nil {
		return nil, 0, err
	}
	known := map[string]bool{}
	for name := range vocab {
		known[name] = true
	}
	for name := range sse {
		known[name] = true
	}
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}

	var out []Finding
	checked := 0
	for _, rel := range files {
		vocabPrefix := ""
		for home, prefix := range frameHomes {
			if strings.HasPrefix(rel, home) {
				vocabPrefix = prefix
				break
			}
		}
		if vocabPrefix == "" {
			continue
		}
		for i, line := range read(root, rel) {
			// A comment documenting a frame's shape is not a frame. This mattered the day the clone
			// stream was sealed: its handler documents the four event shapes above the code, and a rule
			// that reads prose as source reports a file it cannot fix.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if reAppFrame.MatchString(line) {
				checked++
				continue
			}
			if m := reAppFrameLiteral.FindStringSubmatch(line); m != nil {
				checked++
				out = append(out, Finding{
					File: rel, Line: i + 1,
					Detail: "appFrame is given a string where a types.WSMessageType belongs",
					Why: "the builder is the one place the frame's shape is decided; handing it a literal puts the " +
						"name back outside the vocabulary the parity rule reads",
				})
				continue
			}
			m := reFrameType.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			checked++
			name := m[1]
			why := "the name exists as a types.WSType constant and this is a second spelling of it; the two may " +
				"drift apart and nothing compares them"
			if !known[name] {
				why = "the name is in no frame vocabulary, so the wsevents parity rule never sees it and " +
					"nothing checks that the frontend reads this broadcast"
			}
			out = append(out, Finding{
				File: rel, Line: i + 1,
				Detail: fmt.Sprintf("frame type %q is written inline, not as a types.%s constant", name, vocabPrefix),
				Why:    why,
			})
		}
	}
	// Second pass: events named anywhere in the tree by the StreamEvent shape. A frame built outside a
	// home is either a protocol this rule has not been told about or an event that left the vocabulary.
	for _, rel := range files {
		for i, line := range read(root, rel) {
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if reStreamEventConst.MatchString(line) {
				checked++
				continue
			}
			m := reStreamEventLiteral.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			checked++
			name := m[1]
			why := "the name is emitted as a literal, so the vocabulary in backend/types does not contain it " +
				"and wsevents cannot compare it against what the browser dispatches"
			if known[name] {
				why = "a types.WSType constant already spells this name; the literal is a second spelling " +
					"nothing compares it against"
			}
			out = append(out, Finding{
				File: rel, Line: i + 1,
				Detail: fmt.Sprintf("event %q is emitted by a literal StreamEvent Type, not a types.WSType constant", name),
				Why:    why,
			})
		}
	}

	if checked == 0 {
		return nil, 0, fmt.Errorf("no frame built in %v — the matcher is blind, not the tree", sortedHomes())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, checked, nil
}

// sseEvents collects the clone stream's vocabulary. The two digits are the floor: four names exist, and
// finding one means the declaration shape changed under the pattern.
func sseEvents(root string) (map[string]bool, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, rel := range files {
		for _, line := range read(root, rel) {
			if m := reSSEConst.FindStringSubmatch(line); m != nil {
				out[m[1]] = true
			}
		}
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("only %d SSEType* constants found — the matcher is wrong, not the tree", len(out))
	}
	return out, nil
}

func sortedHomes() []string {
	out := make([]string, 0, len(frameHomes))
	for home := range frameHomes {
		out = append(out, home)
	}
	sort.Strings(out)
	return out
}
