package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MIKE reads only the image refs a Dockerfile names, because the host has to fetch every one: the build container
// gets no registry credentials. Nothing else in the file is MIKE's business — it is untrusted text that builds
// inside gVisor, and it reaches the builder unchanged (docs/adr/0002).

// BuildFile is MIKE's reading of a project Dockerfile.
type BuildFile struct {
	// Bases names every image ref the file needs, in file order, deduped, with stage names and `scratch`
	// left out. These are the images the host hands in.
	Bases []string
	// FirstFrom is the file's first `FROM`, resolved the way `Bases` is, `scratch` included — the name to put
	// in a message about this build.
	FirstFrom string
}

// ParseBuildFile answers one question and touches nothing: which images must the host fetch for this build.
// It runs no podman and consults no environment.
//
// A ref spelled with a build arg resolves from the file's own `ARG` default, the only value the builder can give
// it: MIKE hands over no `--build-arg`. A ref that cannot become a name is refused, because MIKE cannot fetch an
// image it cannot name.
func ParseBuildFile(content string) (*BuildFile, error) {
	logical, err := buildFileLogicalLines(content)
	if err != nil {
		return nil, err
	}

	bf := &BuildFile{}
	// Stages are addressed by name or by index, and both spellings mean "bytes this file produces", not
	// "an image the host owes the build".
	stages := map[string]bool{}
	var stageOrder []string
	sawFrom := false
	// Only an `ARG` written before the first `FROM` can name a base. One declared inside a stage cannot name
	// the image that stage builds on, and it is invisible to a later stage's `FROM` — the builder's own
	// scoping, measured on both engines, so mirroring it is the whole rule.
	buildArgs := map[string]string{}

	for _, ln := range logical {
		instr, err := parseBuildInstruction(ln.text, ln.line)
		if err != nil {
			return nil, err
		}
		switch instr.Instruction {
		case "ARG":
			if sawFrom || len(instr.Args) == 0 {
				continue
			}
			name, value, declared := strings.Cut(instr.Args[0], "=")
			if !declared || !isBuildArgName(name) {
				// `ARG NAME` with no default is the builder's empty string, which MIKE cannot fetch. It is
				// left out of the map so the `FROM` that uses it is refused by name.
				continue
			}
			// Kept as written: a default is expanded only when a base asks for it, so a broken `ARG` this
			// file never uses is nobody's refusal.
			buildArgs[name] = value
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
			if strings.Contains(ref, "$") {
				ref, err = expandBuildArgRef(ref, buildArgs, map[string]bool{}, instr.Line, "FROM")
				if err != nil {
					return nil, err
				}
			}
			if bf.FirstFrom == "" {
				bf.FirstFrom = ref
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
		case "COPY", "ADD":
			from, ok := instr.Flags["from"]
			if !ok || strings.TrimSpace(from) == "" {
				continue
			}
			named, err := copyFromNamesStage(from, stages, stageOrder, instr.Line, instr.Instruction)
			if err != nil {
				return nil, err
			}
			if named {
				continue
			}
			if strings.Contains(from, "$") {
				from, err = expandBuildArgRef(from, buildArgs, map[string]bool{}, instr.Line, instr.Instruction)
				if err != nil {
					return nil, err
				}
				// The value can name a stage, and the check above could not know that yet.
				if named, err = copyFromNamesStage(from, stages, stageOrder, instr.Line, instr.Instruction); err != nil {
					return nil, err
				} else if named {
					continue
				}
			}
			bf.addBase(from)
		}
	}

	if !sawFrom {
		return nil, buildRefuse(1, "", "no FROM instruction", "start the Dockerfile with the image to build on")
	}
	return bf, nil
}

// copyFromNamesStage reports whether a `COPY --from` value names bytes this file produces rather than an image
// the host owes: a stage name, an absolute path, or an index into the stages reached so far.
func copyFromNamesStage(from string, stages map[string]bool, stageOrder []string, line int, instruction string) (bool, error) {
	if stages[strings.ToLower(from)] || strings.HasPrefix(from, "/") || strings.ContainsAny(from, " \t") {
		return true, nil
	}
	if idx, err := strconv.Atoi(from); err == nil {
		if idx < 0 || idx >= len(stageOrder) {
			return false, buildRefuse(line, instruction,
				fmt.Sprintf("copies from stage %d, which this file has not reached", idx),
				"copy from a stage defined above this line")
		}
		return true, nil
	}
	return false, nil
}

// addBase records an image the host owes the build, keeping file order and one copy of each. `scratch` is
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

// The bounds exist because this reads a repository's text. A default is resolved in full before it substitutes, so
// one pass settles what can settle and the second proves the rest cannot; the length bound is what stops nested
// `ARG A=${B}${B}` lines, each level measured before it is accepted.
const (
	buildExpandMaxPasses = 2
	buildExpandMaxLen    = 1024
)

var buildArgRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// errBuildArgCycle marks a default that borrows itself, which is a file the builder would leave empty and MIKE
// cannot name.
var errBuildArgCycle = errors.New("a build arg that borrows itself")

// isBuildArgName reports whether a name is one the builder would accept. A name shaped any other way is not a
// build arg, so it names nothing and expands to nothing.
func isBuildArgName(name string) bool {
	return name != "" && buildArgRefRe.MatchString("${"+name+"}")
}

// expandBuildArgRef replaces every `$NAME` and `${NAME}` with the value this file gave it. What it cannot expand
// is a refusal rather than a guess: the builder would expand the line its own way, and MIKE would have fetched
// something else.
//
// `visiting` holds the args already being resolved on this path, which is how a default that borrows itself is
// caught instead of recursed forever.
func expandBuildArgRef(s string, args map[string]string, visiting map[string]bool, line int, instruction string) (string, error) {
	for pass := 0; pass < buildExpandMaxPasses; pass++ {
		if !strings.Contains(s, "$") {
			if s == "" {
				return "", buildRefuse(line, instruction, "expands to nothing",
					"give the `ARG` a default that names an image")
			}
			return s, nil
		}
		missing, failed := "", ""
		var deep error
		out := buildArgRefRe.ReplaceAllStringFunc(s, func(match string) string {
			groups := buildArgRefRe.FindStringSubmatch(match)
			name := groups[1]
			if name == "" {
				name = groups[2]
			}
			if failed != "" || deep != nil {
				return match
			}
			if _, ok := args[name]; !ok {
				if missing == "" {
					missing = name
				}
				return match
			}
			resolved, err := buildArgValue(name, args, visiting, line, instruction)
			if err != nil {
				if errors.Is(err, errBuildArgCycle) {
					failed = name
				} else {
					deep = err
				}
				return match
			}
			return resolved
		})
		if deep != nil {
			return "", deep
		}
		if failed != "" {
			return "", buildRefuse(line, instruction, "expands a build arg that never settles ($"+failed+")",
				"write the ref out instead of composing it from build args")
		}
		if out == s {
			if missing == "" {
				return "", buildRefuse(line, instruction, "reads its image from "+s+", which is not a plain $NAME",
					"write the ref out, or use a bare `${NAME}` with an `ARG` default above the `FROM`")
			}
			return "", buildRefuse(line, instruction, "reads its image from "+s+", and $"+missing+" has no value in this file",
				"give `ARG "+missing+"` a default above the `FROM` line: MIKE hands the builder no build args of its own")
		}
		if len(out) > buildExpandMaxLen {
			return "", buildRefuse(line, instruction,
				"expands an image ref past "+strconv.Itoa(buildExpandMaxLen)+" characters",
				"write the ref out instead of composing it from build args")
		}
		s = out
	}
	return "", buildRefuse(line, instruction, "expands a build arg that never settles", "write the ref out")
}

// buildArgValue resolves one arg's default, expanding the defaults it borrows. A default that borrows itself
// comes back as errBuildArgCycle for the caller on that path to turn into a refusal.
func buildArgValue(name string, args map[string]string, visiting map[string]bool, line int, instruction string) (string, error) {
	if visiting[name] {
		return "", errBuildArgCycle
	}
	value := args[name]
	if !strings.Contains(value, "$") {
		return value, nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	return expandBuildArgRef(value, args, visiting, line, instruction)
}

// buildLogicalLine is one instruction with its continuations joined, remembering the line it began on.
type buildLogicalLine struct {
	line int
	text string
}

// buildFileLogicalLines joins backslash continuations and drops comments and blanks.
//
// Only the default escape character is accepted: `# escape=` changes where a line ends, and a line split
// wrongly is a different instruction from the one the builder reads.
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

// buildInstruction is one instruction: its name, the flags in front of it, and the argument text as written.
type buildInstruction struct {
	Line        int
	Instruction string
	Flags       map[string]string
	Args        []string
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
	instr.Args = nil
	remainder := readBuildFlags(instr, rest)

	words, err := buildShellWords(remainder)
	if err != nil {
		return nil, buildRefuse(line, instr.Instruction, "has an unclosed quote", "close the quote")
	}
	instr.Args = words
	return instr, nil
}

// readBuildFlags consumes the leading `--flag`/`--flag=value` tokens into instr.Flags and returns the rest of
// the line untouched.
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

// buildArgIndex finds a bare keyword among the arguments, case-insensitively (`AS`, and nothing else today).
func buildArgIndex(args []string, keyword string) int {
	for i, a := range args {
		if strings.EqualFold(a, keyword) {
			return i
		}
	}
	return -1
}

// firstBuildToken splits the leading whitespace-delimited token from the rest of the line, quotes kept.
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

// buildShellWords splits an argument list on whitespace outside quotes. It is not a shell: no expansion, no
// command substitution, no `$` handling — a token containing one stays text.
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

// BuildFileError is a refusal naming the line, what is wrong with it, and what to do instead.
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
