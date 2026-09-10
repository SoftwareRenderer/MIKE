package main

// The rules. Each one scans production Go for a shape the codebase is leaving behind, and reports
// every instance: the baseline decides which of them are already paid for.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule is one structural check plus its name and the reason it exists.
//
// Scan returns the population it examined as well as the findings, because "0 violations" means
// nothing without it: a rule whose matcher stopped matching reports exactly the same number as a
// rule that has nothing left to fix. A population of 0 is a failure, not a clean run.
type Rule struct {
	Name        string
	Description string
	Scan        func(root string) (findings []Finding, checked int, err error)
}

// rules is the registry, and -rule list prints it.
var rules = []Rule{
	{Name: "http", Description: "net/http imported by a package that is not transport", Scan: scanHTTP},
	{Name: "env", Description: "environment read outside the startup resolution points", Scan: scanEnv},
	{Name: "locks", Description: "keyed mutex table written by hand", Scan: scanLockTables},
	{Name: "routes", Description: "route registered without a method, or dispatching on r.Method", Scan: scanRoutes},
	{Name: "writeselect", Description: "state write reachable from a GET that is not one statement", Scan: scanCancellableWrites},
	{Name: "wsevents", Description: "app-stream event vocabulary, Go vs TypeScript", Scan: scanWSEvents},
	{Name: "wsframe", Description: `app-stream frame names built from a types.WSType constant, not a literal`, Scan: scanWSFrameLiterals},
	{Name: "envassign", Description: "cmd.Env assigned without going through an environment allowlist", Scan: scanEnvAssignment},
	{Name: "status500", Description: "500 whose body is built from an error value instead of \"internal error\"", Scan: scanStatusEcho},
	{Name: "ctxroot", Description: "base context (context.Background/TODO) not classified root|detached|legacy", Scan: scanContextRoots},
}

// selectRules resolves -rule. An unknown name is an error rather than a silent no-op: a typo in a
// gate invocation that runs nothing is how a gate goes blind while printing "ok".
func selectRules(all []Rule, name string) ([]Rule, error) {
	if name == "" {
		return all, nil
	}
	if name == "list" {
		for _, r := range all {
			fmt.Printf("%-12s %s\n", r.Name, r.Description)
		}
		os.Exit(0)
	}
	for _, r := range all {
		if r.Name == name {
			return []Rule{r}, nil
		}
	}
	names := make([]string, 0, len(all))
	for _, r := range all {
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("unknown rule %q (known: %s)", name, strings.Join(names, ", "))
}

// skipDirs are trees that are not this repository's source: build output, dependencies, and the
// worktree checkouts MIKE creates for its own tasks (which contain other people's Go code).
// skipDirs also excludes this tool's own directory: the http rule matches the literal "net/http"
// that lives inside its own regexp, and a checker that reports itself has stopped describing the
// codebase.
var skipDirs = map[string]bool{
	"node_modules": true, "dist": true, "testdata": true, ".worktrees": true,
	"vendor": true, ".git": true, "__pycache__": true, "archgate": true,
}

// goFiles returns the production Go files under the scanned roots — never _test.go. A rule that
// should see tests says so by reading the file itself.
func goFiles(root string) ([]string, error) {
	var out []string
	roots := []string{
		filepath.Join(root, "backend"),
		filepath.Join(root, "pkg"),
		filepath.Join(root, "scripts"),
	}
	for _, dir := range roots {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // an unreadable corner of the tree is not a violation
			}
			if info.IsDir() {
				if skipDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return nil
				}
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// The root package: main.go and its startup config, where env reads are allowed by design.
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	// A guard, not a statistic: every rule here scans this one list, so a walker that quietly finds
	// nothing turns six rules green at once. The tree has hundreds of production Go files; anything
	// under this number means the walk broke, and the run must say so instead of reporting clean.
	if len(out) < minGoFiles {
		return nil, fmt.Errorf("found only %d production Go files under %s — refusing to report clean", len(out), roots[0])
	}
	return out, nil
}

// minGoFiles is well under the real count (168 today) and far above what a broken walk returns. A
// var, not a const, so a detection test can lay out a three-file fixture; the guard has its own
// test, so opting out here does not leave it unproven.
var minGoFiles = 100

// read returns a file's lines, or nil when it cannot be read (a scanned path that moved is not a
// violation; the caller simply finds nothing there).
func read(root, rel string) []string {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return strings.Split(string(raw), "\n")
}

var (
	reNetHTTP    = regexp.MustCompile(`^\s*"net/http(/httptest)?"\s*$|^\s*(\w+)\s+"net/http(/httptest)?"\s*$`)
	reHTTPImport = regexp.MustCompile(`"net/http"`)
	reEnvRead    = regexp.MustCompile(`\bos\.(Getenv|LookupEnv|Environ|Getenvs)\(`)
	// The type may end the line (no trailing comment), so nothing may be required after it.
	reLockField = regexp.MustCompile(`^\s*(\w+)\s+(sync\.Map|map\[string\]\*sync\.Mutex)\b`)
)

// httpHome are the packages whose job is the transport: they may import net/http without a
// baseline entry. Everything else is either already baselined or a new violation.
var httpHome = map[string]bool{
	"backend/server":   true,
	"backend/httputil": true,
	"root":             true, // main.go: the composition root that builds the listener it hands to Serve
}

// scanHTTP finds domain packages that import net/http, which is the seam F2 measured: eight
// packages make HTTP decisions, and only two of them are transport.
func scanHTTP(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	for _, rel := range files {
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = "root"
		}
		if httpHome[dir] {
			continue
		}
		for i, line := range read(root, rel) {
			if !reHTTPImport.MatchString(line) {
				continue
			}
			out = append(out, Finding{
				File: rel, Line: i + 1, Detail: `imports "net/http"`,
				Why: "a domain package should answer with a typed result and let backend/server choose the status; measured in TODO.md F2",
			})
			break // one finding per file: the violation is the import, not its uses
		}
	}
	return out, len(files), nil
}

// envHome lists the files allowed to read the environment: the resolution ladder itself. The rule
// is F7 — configuration must be resolved once at startup and passed down, because a call-time read
// makes behaviour depend on when a function happened to run.
var envHome = map[string]bool{
	"main.go": true,
	// The ladder itself: every MIKE_* name is read once here and reaches its consumer as a decision.
	// This is the file the rule points at rather than an exemption from it.
	"backend/startup/config.go": true,
	// The one mechanism that iterates the process environment for the subprocess allowlists. It
	// names no variable — the caller hands in the filter — so it cannot read a configuration value
	// at call time, which is what this rule exists to catch.
	"pkg/subproc/subproc.go": true,
}

// scanEnv finds os.Getenv / LookupEnv / Environ outside the resolution points.
func scanEnv(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	for _, rel := range files {
		if envHome[rel] {
			continue
		}
		seen := map[string]bool{}
		for i, line := range read(root, rel) {
			m := reEnvRead.FindStringSubmatch(line)
			if m == nil || strings.Contains(line, "//") {
				continue
			}
			// Key on the variable name where the call names one, so two different variables in
			// one file are two violations and an unrelated edit does not merge them.
			detail := "os." + m[1]
			if name := envVarName(line); name != "" {
				detail += "(" + name + ")"
			}
			if seen[detail] {
				continue
			}
			seen[detail] = true
			out = append(out, Finding{
				File: rel, Line: i + 1, Detail: detail,
				Why: "read it once in backend/startup (the flag -> env -> default ladder) and pass the decision in; a call-time read makes behaviour depend on when the function ran — see F7",
			})
		}
	}
	return out, len(files), nil
}

// envVarName extracts MIKE_* / DANGER_ZONE from a line so a baseline key names the variable.
func envVarName(line string) string {
	m := regexp.MustCompile(`"(MIKE_[A-Z_0-9]+|DANGER_ZONE)"`).FindStringSubmatch(line)
	if m != nil {
		return m[1]
	}
	m2 := regexp.MustCompile(`\b(\w*Env)\b`).FindStringSubmatch(line)
	if m2 != nil {
		return m2[1]
	}
	return ""
}

// scanLockTables finds the per-key lock tables F5 counted: sync.Map fields named like a mutex, and
// map[string]*sync.Mutex. Each one re-implements LoadOrStore, and each one is a table someone has
// to remember to key correctly.
func scanLockTables(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	for _, rel := range files {
		for i, line := range read(root, rel) {
			m := reLockField.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1]
			looksLikeALock := strings.HasSuffix(name, "Mu") || strings.HasSuffix(name, "Mux") ||
				strings.HasSuffix(name, "Locks") || strings.HasSuffix(name, "Mutex")
			if !looksLikeALock {
				continue
			}
			out = append(out, Finding{
				File: rel, Line: i + 1, Detail: "keyed lock table " + name,
				Why: "use pkg/keylock with one named type per lock domain (F5/F16); a hand-written table loses the distinction between domains",
			})
		}
	}
	return out, len(files), nil
}

// --- 7. envassign ------------------------------------------------------------
//
// Assigning to cmd.Env *replaces* the child's environment, so the mistake is not "an extra variable"
// but "the allowlist is gone". This repo has the scar: `os.Environ()` on an agent subprocess leaked
// MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY through /proc/self/environ to a binary an extension
// supplied. Three allowlists exist (git.Env/CloneEnv, sandboxruntime.PodmanEnv, agent.SafeAgentEnv)
// plus one sanctioned widening (term.hostShellEnv, under DANGER_ZONE=1); an assignment that names
// none of them is the leak pattern, whatever it was written for.
//
// Zero baseline: nothing in the tree does this today, so a new one fails rather than joining a register.

var reEnvAssign = regexp.MustCompile(`^\s*\w+\.Env\s*=\s*(.+)$`)

// reEnvAllowed matches an assignment whose right-hand side came from an allowlist builder. Matching
// the call rather than the file is deliberate: git's own clone.go and worktree.go legitimately rebuild
// the environment, and they do it through git.Env.
var reEnvAllowed = regexp.MustCompile(`Env\(|hostShellEnv\(|subproc\.Inherit\(`)

func scanEnvAssignment(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	checked := 0
	for _, rel := range files {
		// The mechanism itself assigns cmd.Env; that is the one place it is allowed to.
		if strings.HasPrefix(rel, "pkg/subproc/") {
			continue
		}
		for i, line := range read(root, rel) {
			m := reEnvAssign.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			checked++
			if reEnvAllowed.MatchString(m[1]) {
				continue
			}
			out = append(out, Finding{
				File: rel, Line: i + 1, Detail: "cmd.Env = " + strings.TrimSpace(m[1]),
				Why: "a bare list replaces the child's environment; build it from git.Env/CloneEnv, " +
					"sandboxruntime.PodmanEnv or agent.SafeAgentEnv (term.hostShellEnv is the one widening)",
			})
		}
	}
	if checked == 0 {
		return nil, 0, fmt.Errorf("no cmd.Env assignment found under %s — the matcher is blind, not the tree", root)
	}
	return out, checked, nil
}

// reStatusEcho finds the identifier whose text is being interpolated into a response body.
var reStatusEcho = regexp.MustCompile(`%(?:[vqs])\\?",\s*([A-Za-z_][A-Za-z0-9_.]*)|([A-Za-z_][A-Za-z0-9_.]*)\.Error\(\)`)

// scanStatusEcho reports a 500 whose body is built from an error value.
//
// A 500 means the server could not tell what the caller did wrong; that is why httputil.Fail answers
// "internal error" and logs the detail. Echoing `err.Error()` instead does two things at once: it ships
// paths, driver text and remote-host output to the browser, and it reports a user-caused condition
// (already running, a setting the operator has to change) as a server fault, because the only status the
// call site knew was 500. Both were measured in 2i: the sentence the browser saw was the same one the
// log had, and the two cases the code *did* mean for the user were unreachable misclassifications.
//
// The fix is never to keep the echo: give the error a class in backend/errs (errs.Conflict keeps the
// producer's sentence and answers 409) and answer through httputil.Fail. A 4xx echo is not reported — a
// validation refusal's message was written for the person who triggered it.
func scanStatusEcho(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	checked := 0
	for _, rel := range files {
		lines := read(root, rel)
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			if !strings.Contains(line, "JSONError(w, http.Status") {
				continue
			}
			checked++
			if !strings.Contains(line, "http.StatusInternalServerError") {
				continue
			}
			// The message argument is often on the following line.
			text := line
			for depth := 0; unbalanced(text) && depth < 3 && i+depth+1 < len(lines); depth++ {
				text += " " + strings.TrimSpace(lines[i+depth+1])
			}
			m := reStatusEcho.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			ident := m[1]
			if ident == "" {
				ident = m[2]
			}
			if !strings.Contains(strings.ToLower(ident), "err") {
				continue
			}
			out = append(out, Finding{
				File: rel, Line: i + 1, Detail: "500 echoes " + ident,
				Why: "a 500's body is \"internal error\" and the detail goes to the log; classify the error in backend/errs (errs.Conflict keeps the caller's sentence) and answer through httputil.Fail",
			})
		}
	}
	return out, checked, nil
}

// unbalanced reports whether an opened call continues on the next line.
func unbalanced(text string) bool {
	return strings.Count(text, "(") > strings.Count(text, ")")
}
