package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// MIKE reads only the image refs a Dockerfile names and the commands it can run: the host must fetch every image,
// and a person must approve every command. The file itself goes to the builder unchanged, because the sandbox is
// what limits it (docs/adr/0002).

// BuildFile is MIKE's reading of a project Dockerfile.
type BuildFile struct {
	// Bases names every image ref the file needs, in file order, deduped, with stage names and `scratch`
	// left out. These are the images the host hands in.
	Bases []string
	// FirstFrom is the file's first `FROM` as written, `scratch` included — the name to put in a message
	// about this build.
	FirstFrom string
	// Runs holds every command the file can run, in file order: each `RUN`, and each `ONBUILD RUN` payload.
	Runs []string
	// Shells holds the text of each `SHELL` line. The shell decides what a line in Runs really executes, so
	// approving the commands without it would be approving something else.
	Shells []string
}

// BuildFileError is a refusal that names the line, what is wrong with it and what to do instead. A refusal that
// only said "unsupported" would send the reader to this parser to find out why.
type BuildFileError struct {
	Line        int
	Instruction string
	Reason      string
	Fix         string
}

func (e *BuildFileError) Error() string {
	if e.Fix == "" {
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Instruction, e.Reason)
	}
	return fmt.Sprintf("line %d: %s: %s — %s", e.Line, e.Instruction, e.Reason, e.Fix)
}

func buildRefuse(line int, instr, reason, fix string) error {
	return &BuildFileError{Line: line, Instruction: instr, Reason: reason, Fix: fix}
}

// ParseBuildFile reads the two things MIKE needs, touching no file, running no podman and expanding no
// variable. A `$VAR` in `FROM` or `--from` is therefore refused rather than resolved: MIKE cannot fetch an image
// it cannot name.
func ParseBuildFile(content string) (*BuildFile, error) {
	logical, err := buildFileLogicalLines(content)
	if err != nil {
		return nil, err
	}

	bf := &BuildFile{}
	// Stages are addressed by name or by index, and both spellings mean "bytes this file produces", not
	// "an image the host has to hand in".
	stages := map[string]bool{}
	var stageOrder []string
	sawFrom := false

	for _, ln := range logical {
		instr, err := parseBuildInstruction(ln.text, ln.line)
		if err != nil {
			return nil, err
		}
		switch instr.Instruction {
		case "FROM":
			sawFrom = true
			if value, ok := instr.Flags["platform"]; ok {
				return nil, buildRefuse(instr.Line, "FROM",
					fmt.Sprintf("asks for the platform %q", value),
					"drop --platform: MIKE pulls the base on this host and hands in the architecture this host has")
			}
			if len(instr.Args) == 0 {
				return nil, buildRefuse(instr.Line, "FROM", "names no image", "give it an image ref")
			}
			ref := instr.Args[0]
			if bf.FirstFrom == "" {
				bf.FirstFrom = ref
			}
			if strings.Contains(ref, "$") {
				return nil, buildRefuse(instr.Line, "FROM", "builds its image "+ref+" from a variable",
					"write the ref out: MIKE fetches each named image and hands it in, so it has to be a name")
			}
			// `FROM x AS name` defines a stage, and a later `FROM name` or `--from=name` is that stage, not
			// an image the host owes the build.
			if i := buildArgIndex(instr.Args, "AS"); i >= 0 && i+1 < len(instr.Args) {
				stages[strings.ToLower(instr.Args[i+1])] = true
			}
			stageOrder = append(stageOrder, ref)
			if !stages[strings.ToLower(ref)] {
				bf.addBase(ref)
			}
		case "RUN":
			bf.Runs = append(bf.Runs, instr.Text)
		case "ONBUILD":
			// The trigger fires in a build of an image derived from this one, which for a project image
			// means one of its own later builds. It runs, so it is in the approved set.
			if rest := strings.TrimSpace(instr.Text); len(rest) > 4 && strings.EqualFold(rest[:4], "RUN ") {
				bf.Runs = append(bf.Runs, strings.TrimSpace(rest[4:]))
			}
		case "SHELL":
			bf.Shells = append(bf.Shells, instr.Text)
		case "COPY", "ADD":
			from, ok := instr.Flags["from"]
			if !ok || strings.TrimSpace(from) == "" {
				continue
			}
			// An absolute `--from` is a path, a name in `stages` or an index into `stageOrder` is a stage of
			// this file, and none of them is an image the host has to hand in.
			if stages[strings.ToLower(from)] || strings.HasPrefix(from, "/") || strings.ContainsAny(from, " \t") {
				continue
			}
			if idx, err := strconv.Atoi(from); err == nil {
				if idx < 0 || idx >= len(stageOrder) {
					return nil, buildRefuse(instr.Line, instr.Instruction,
						fmt.Sprintf("copies from stage %d, which this file has not reached", idx),
						"copy from a stage defined above this line")
				}
				continue
			}
			if strings.Contains(from, "$") {
				return nil, buildRefuse(instr.Line, instr.Instruction, "copies from the variable image "+from,
					"write the ref out: MIKE fetches each named image and hands it in, so it has to be a name")
			}
			bf.addBase(from)
		}
	}

	if !sawFrom {
		return nil, buildRefuse(1, "", "no FROM instruction", "start the Dockerfile with the image to build on")
	}
	return bf, nil
}

// addBase records an image the host owes the build, keeping file order and one copy of each.
//
//	`scratch` is
//
// the empty image the builder already has.
func (b *BuildFile) addBase(ref string) {
	if ref == "" || strings.EqualFold(ref, "scratch") {
		return
	}
	for _, have := range b.Bases {
		if have == ref {
			return
		}
	}
	b.Bases = append(b.Bases, ref)
}

// RunSetHash identifies the command set a person approved, so a change needs a fresh confirmation. `SHELL` lines
// join it because they change what a `RUN` actually executes.
func (b *BuildFile) RunSetHash() string {
	h := sha256.New()
	for _, s := range b.Shells {
		fmt.Fprintf(h, "shell\x00%d\x00%s\x00", len(s), s)
	}
	for _, r := range b.Runs {
		fmt.Fprintf(h, "%d\x00%s\x00", len(r), r)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// buildLogicalLine is one instruction with its continuations joined, remembering the line it began
//
//	on.
type buildLogicalLine struct {
	line int
	text string
}

// buildFileLogicalLines joins backslash continuations and drops comments and blanks.
//
// Only the default escape character is accepted: `# escape=` changes where a line ends, and a line split wrongly
// is a command missing from the approved set.
func buildFileLogicalLines(content string) ([]buildLogicalLine, error) {
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []buildLogicalLine
	for i := 0; i < len(raw); i++ {
		text := strings.TrimSpace(raw[i])
		if strings.HasPrefix(strings.ToLower(text), "#escape=") || strings.HasPrefix(strings.ToLower(text), "# escape=") {
			value := strings.TrimSpace(text[strings.Index(text, "=")+1:])
			if value != `\` {
				return nil, buildRefuse(i+1, "# escape=", "changes the line-continuation character",
					"remove the directive; MIKE reads only the default `\\` continuation")
			}
			continue
		}
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		start := i + 1
		for strings.HasSuffix(text, "\\") && i+1 < len(raw) {
			text = strings.TrimRight(text, "\\ ") + " " + strings.TrimSpace(raw[i+1])
			i++
		}
		out = append(out, buildLogicalLine{line: start, text: text})
	}
	return out, nil
}

// buildInstruction is one instruction: its name, the flags in front of it, and the argument text
//
//	as written.
type buildInstruction struct {
	Line        int
	Instruction string
	Flags       map[string]string
	Args        []string
	// Text is the argument text with the flags removed. For `RUN` it is the command verbatim, which is what
	// the approved set is built from — a re-quoted command is a different set rather than a coincidentally
	// identical one.
	Text string
}

// parseBuildInstruction splits one logical line into name, leading flags and the rest. Flags are read but not
// judged: the builder acts on them, inside the sandbox.
func parseBuildInstruction(text string, line int) (*buildInstruction, error) {
	name, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	instr := &buildInstruction{
		Line:        line,
		Instruction: strings.ToUpper(strings.TrimSpace(name)),
		Flags:       map[string]string{},
	}
	remainder := readBuildFlags(instr, rest)
	instr.Text = strings.TrimSpace(remainder)

	words, err := buildShellWords(remainder)
	if err != nil {
		return nil, buildRefuse(line, instr.Instruction, "has an unclosed quote", "close the quote")
	}
	instr.Args = words
	if instr.Instruction == "RUN" && instr.Text == "" {
		return nil, buildRefuse(line, "RUN", "has no command",
			"give it a command, or remove the line — an empty RUN is not in the set a person approved")
	}
	return instr, nil
}

// readBuildFlags consumes the leading `--flag`/`--flag=value` tokens into instr.Flags and returns
//
//	the rest of
//
// the line untouched, which matters twice: the `RUN` text is the approved set, and a command
//
//	reconstructed
//
// from tokens would survive a change of quoting.
func readBuildFlags(instr *buildInstruction, rest string) string {
	rest = strings.TrimLeft(rest, " \t")
	for strings.HasPrefix(rest, "--") {
		token, remainder := firstBuildToken(rest)
		flag, value, _ := strings.Cut(strings.TrimPrefix(token, "--"), "=")
		instr.Flags[strings.ToLower(flag)] = value
		rest = strings.TrimLeft(remainder, " \t")
	}
	return rest
}

// buildArgIndex finds a bare keyword among the arguments, case-insensitively (`AS`, and nothing
//
//	else today).
func buildArgIndex(args []string, keyword string) int {
	for i, a := range args {
		if strings.EqualFold(a, keyword) {
			return i
		}
	}
	return -1
}

// firstBuildToken splits the leading whitespace-delimited token from the rest of the line, quotes
//
//	kept.
func firstBuildToken(s string) (token, rest string) {
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// buildShellWords splits an argument list on whitespace outside quotes. It is deliberately not a
//
//	shell: no
//
// expansion, no command substitution, no `$` handling — a token containing one stays text.
func buildShellWords(s string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		quote rune
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}
