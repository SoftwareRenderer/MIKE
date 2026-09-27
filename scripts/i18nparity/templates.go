package main

// Prompt-template parity.
//
// A locale's prompts/ is an overlay: the executor renders the same templates with the same
// PromptData, so a translated pack may change prose and nothing else. Comparing skeletons — the
// control flow and data references with the text removed — is the whole check. It is done with
// text/template's own parser, mirroring prompts.RenderSystemPrompt, so the checker cannot pass a
// template the runtime would reject.
//
// Note on what parse does NOT catch: `{{template "plan-mode" .}}` naming a define that does not
// exist parses fine and only fails at execution, because name resolution is deferred. So the
// invoked names are collected and checked against the names the set actually declares.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

// receiverName is the empty template the set is parsed onto, mirroring the "prompts" receiver in
// prompts.RenderSystemPrompt. It carries no content and is not a define.
const receiverName = "prompts"

// promptTemplates is the English set the overlay must mirror, named as the loaders name them
// (backend/prompts renders base/merge_conflict/summarize/summarize_user/answer_question/
// answer_question_user directly, and base invokes plan-mode and explore-mode).
var promptTemplates = []string{
	"base.tmpl",
	"plan-mode.tmpl",
	"explore-mode.tmpl",
	"merge_conflict.tmpl",
	"summarize.tmpl",
	"summarize_user.tmpl",
	"answer_question.tmpl",
	"answer_question_user.tmpl",
}

// promptFuncs mirrors prompts.funcs. A template calling an unknown function is a parse error, so
// the checker must carry the same set or it rejects a valid overlay.
var promptFuncs = template.FuncMap{
	"inc": func(i int) int { return i + 1 },
}

// TmplSet is one directory of .tmpl files parsed the way the executor parses it.
type TmplSet struct {
	Dir string
	// Files are the .tmpl basenames present, sorted.
	Files []string
	// Skeletons maps a template name (a file root like "base.tmpl", or a define name like
	// "plan-mode") to its control flow and references in document order, text removed.
	Skeletons map[string][]string
	// Invoked lists, per template name, every `{{template "x"}}` target it calls.
	Invoked map[string][]string
	// Declared is the set of names this directory declares.
	Declared map[string]bool
	// Comments counts comment nodes per template name — reported, never required.
	Comments map[string]int
}

// LoadTmplSet parses dir. Per-file parse errors are surfaced first (a set parse would otherwise
// name whichever file happened to sort first).
func LoadTmplSet(dir string) (*TmplSet, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no .tmpl files in %s", dir)
	}
	sort.Strings(matches)

	set := &TmplSet{
		Dir:       dir,
		Skeletons: map[string][]string{},
		Invoked:   map[string][]string{},
		Declared:  map[string]bool{},
		Comments:  map[string]int{},
	}
	for _, m := range matches {
		set.Files = append(set.Files, filepath.Base(m))
	}

	for _, m := range matches {
		if _, err := template.New(filepath.Base(m)).Funcs(promptFuncs).ParseFiles(m); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(m), unwrapTmplErr(err))
		}
	}

	tmpl, err := template.New("prompts").Funcs(promptFuncs).ParseFiles(matches...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, unwrapTmplErr(err))
	}

	for _, t := range tmpl.Templates() {
		if t.Tree == nil || t.Tree.Root == nil {
			continue
		}
		if t.Name() == receiverName {
			continue // the empty receiver template, not a file and not a define
		}
		var skel []string
		var invoked []string
		set.walk(t.Tree.Root, &skel, &invoked, t.Name())
		set.Skeletons[t.Name()] = skel
		set.Invoked[t.Name()] = invoked
		set.Declared[t.Name()] = true
	}
	return set, nil
}

// unwrapTmplErr collapses the wrapped error chain text/template produces so the message names the
// file and the line rather than "template: multiple definition of template".
func unwrapTmplErr(err error) error {
	if mw, ok := err.(interface{ Unwrap() []error }); ok {
		parts := mw.Unwrap()
		msgs := make([]string, 0, len(parts))
		for _, p := range parts {
			msgs = append(msgs, p.Error())
		}
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return err
}

func (s *TmplSet) walk(node parse.Node, skel *[]string, invoked *[]string, owner string) {
	switch n := node.(type) {
	case *parse.TextNode:
		// Translated content. This is precisely what a locale pack is allowed to change.
	case *parse.CommentNode:
		s.Comments[owner]++
	case *parse.ActionNode:
		*skel = append(*skel, "act:"+normalizeAction(n.Pipe.String()))
	case *parse.IfNode:
		// `{{else if x}}` is not a distinct node: parse nests a second IfNode inside ElseList,
		// so the recursion reproduces it and the two sides still line up.
		*skel = append(*skel, "if:"+normalizeAction(n.Pipe.String()))
		s.walk(n.List, skel, invoked, owner)
		if n.ElseList != nil {
			*skel = append(*skel, "else")
			s.walk(n.ElseList, skel, invoked, owner)
		}
		*skel = append(*skel, "end")
	case *parse.RangeNode:
		*skel = append(*skel, "range:"+normalizeAction(n.Pipe.String()))
		s.walk(n.List, skel, invoked, owner)
		if n.ElseList != nil {
			*skel = append(*skel, "else")
			s.walk(n.ElseList, skel, invoked, owner)
		}
		*skel = append(*skel, "end")
	case *parse.WithNode:
		*skel = append(*skel, "with:"+normalizeAction(n.Pipe.String()))
		s.walk(n.List, skel, invoked, owner)
		if n.ElseList != nil {
			*skel = append(*skel, "else")
			s.walk(n.ElseList, skel, invoked, owner)
		}
		*skel = append(*skel, "end")
	case *parse.TemplateNode:
		*skel = append(*skel, normalizeAction(n.String()))
		if n.Name != "" {
			*invoked = append(*invoked, n.Name)
		}
	case *parse.BreakNode:
		*skel = append(*skel, "break")
	case *parse.ContinueNode:
		*skel = append(*skel, "continue")
	case *parse.ListNode:
		for _, child := range n.Nodes {
			s.walk(child, skel, invoked, owner)
		}
	default:
		// Unrecognised node kind: recorded so both sides at least compare, and reported by
		// the caller so a newer template feature cannot be silently waved through.
		*skel = append(*skel, "unsupported:"+fmt.Sprintf("%T", node))
	}
}

// normalizeAction collapses whitespace so `{{.X}}` and `{{ .X }}` compare equal while the
// identifier itself must still match byte for byte.
func normalizeAction(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// DefineNames returns the define names (non-file-root templates) of the set, sorted.
func (s *TmplSet) DefineNames() []string {
	var out []string
	for name := range s.Declared {
		if strings.HasSuffix(name, ".tmpl") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ReadFile returns a template file's bytes from the set's directory.
func (s *TmplSet) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.Dir, name))
}
