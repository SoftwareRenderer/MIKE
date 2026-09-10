package main

// archgate measures structural debt in the Go tree and refuses to let it grow.
//
// Every rule here describes a shape the codebase is moving away from, not a shape that is illegal
// today: HTTP decisions in domain packages, environment reads below startup, keyed mutex tables
// written by hand. Banning those outright would fail at HEAD, so each rule runs against a **frozen
// baseline** of the current violations. New violation → red. Violation removed → red until the
// baseline shrinks, which is what makes the debt a register rather than a rumour.
//
// Both directions are enforced, because a check that stopped matching prints the same clean line as
// clean code: a baseline entry whose violation no longer exists is itself a failure (shrink it), and
// a rule that finds zero findings anywhere is reported, not celebrated — see TestRuleFindingNothing.
//
// Keys carry no line numbers (file|detail), so an unrelated edit above a violation does not churn
// the baseline.
//
// Usage:
//
//	go run ./scripts/archgate                 # all rules, compare against baselines
//	go run ./scripts/archgate -rule env       # one rule
//	go run ./scripts/archgate -update         # rewrite baselines from the current tree
//	go run ./scripts/archgate -v              # list every baselined violation, not just the count
//
// Exit codes: 0 clean, 1 findings, 2 inputs missing (a scanned directory is gone).

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Options is the flag set, resolved once.
type Options struct {
	Rule    string // one rule name, or "" for all
	Update  bool   // rewrite baselines instead of comparing
	Verbose bool   // print baselined findings too
	Root    string // repo root
}

func main() {
	opts := Options{}
	flag.StringVar(&opts.Rule, "rule", "", "run one rule by name (see -rule list)")
	flag.BoolVar(&opts.Update, "update", false, "rewrite the baseline from the current tree")
	flag.BoolVar(&opts.Verbose, "v", false, "list baselined violations as well as new ones")
	flag.StringVar(&opts.Root, "root", ".", "repository root")
	flag.Parse()
	os.Exit(run(opts))
}

// run is the whole program, separate from main so tests can drive it against a fixture root.
func run(opts Options) int {
	if opts.Root == "" {
		opts.Root = "."
	}
	if _, err := os.Stat(filepath.Join(opts.Root, "backend")); err != nil {
		fmt.Fprintln(os.Stderr, "archgate: no backend/ under", opts.Root, "— run it from the repo root")
		return 2
	}

	selected, err := selectRules(rules, opts.Rule)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archgate:", err)
		return 2
	}

	bad := 0
	for _, rule := range selected {
		findings, checked, err := rule.Scan(opts.Root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "archgate: %s: scan failed: %v\n", rule.Name, err)
			return 2
		}
		code := report(rule, findings, checked, opts)
		if code != 0 {
			bad = 1
		}
	}
	if bad == 0 {
		fmt.Println("archgate: no new structural violations")
	}
	return bad
}

// report compares one rule's findings to its baseline and prints the verdict.
func report(rule Rule, findings []Finding, checked int, opts Options) int {
	baseline, err := LoadBaseline(filepath.Join(opts.Root, BaselinePath(rule.Name)))
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "archgate: %s: baseline: %v\n", rule.Name, err)
		return 1
	}

	if checked == 0 {
		fmt.Printf("FAIL %s examined nothing — a rule that sees nothing reports clean forever\n", rule.Name)
		return 1
	}

	if opts.Update {
		if err := WriteBaseline(filepath.Join(opts.Root, BaselinePath(rule.Name)), findings); err != nil {
			fmt.Fprintf(os.Stderr, "archgate: %s: write baseline: %v\n", rule.Name, err)
			return 1
		}
		fmt.Printf("%-14s baseline rewritten: %d violation(s)\n", rule.Name, len(findings))
		return 0
	}

	cmp := Compare(findings, baseline)
	if len(cmp.New) == 0 && len(cmp.Stale) == 0 {
		if len(findings) == 0 {
			fmt.Printf("ok   %-12s 0 violations across %d checked — nothing baselined, nothing new\n", rule.Name, checked)
		} else {
			fmt.Printf("ok   %-12s %d baselined violation(s) across %d checked, none new\n", rule.Name, len(findings), checked)
		}
		if opts.Verbose {
			// Location, not Key: a baseline entry tells you the violation is in some file, and a
			// person clearing it has to open the file and look. The key is the ratchet's identity, and
			// the line is the fixer's address.
			for _, f := range findings {
				fmt.Printf("         %s\n", f.Location())
			}
		}
		return 0
	}

	bad := false
	if len(cmp.New) > 0 {
		bad = true
		fmt.Printf("FAIL %s %d NEW violation(s) — this rule is a ratchet, the count only shrinks\n", rule.Name, len(cmp.New))
		for _, f := range cmp.New {
			fmt.Printf("        +%s\n", f.Location())
			if f.Why != "" {
				fmt.Printf("          why: %s\n", f.Why)
			}
		}
	}
	if len(cmp.Stale) > 0 {
		bad = true
		fmt.Printf("FAIL %s %d baseline entries no longer match a violation — you fixed one, shrink the baseline\n", rule.Name, len(cmp.Stale))
		for _, k := range cmp.Stale {
			fmt.Printf("        -%s\n", k)
		}
		fmt.Println("         run: go run ./scripts/archgate -update")
	}
	if bad {
		return 1
	}
	return 0
}
