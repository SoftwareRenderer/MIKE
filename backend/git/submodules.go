package git

// Submodules: a task's worktree is given its submodule working copies as linked worktrees of the person's own
// shared stores, because `git submodule update --init` in a linked worktree re-clones the `.gitmodules` URL
// once per task. A store is found by asking git, never derived from the path, and MIKE never reads a
// `.gitmodules` byte itself: the URL it shows and screens is read out of that file by git's own config reader.
//
// Fetching a repository-chosen URL happens at project import, at a person's click, or automatically for a
// project whose two switches allow it — which of those is the executor's decision, not this package's. What is
// guaranteed here is that a fetch is asked for by the project checkout's own commit and never by a task
// worktree's (the model writes that one), that the URL was screened before git was handed it, and that one
// fetch runs at a time per checkout.
//
// docs/git-operations.md → Submodules: share the person's copy, never re-fetch.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/keylock"
	"mike/pkg/logging"
)

// submoduleModulesDir is the directory a git repository keeps its submodules' gitdirs under.
const submoduleModulesDir = "modules"

// maxSubmoduleWalk bounds the nested walk and the number of submodules one task is given, so a cycle or a
// pathological `.gitmodules` cannot spin this loop or fill the container's mount list.
const maxSubmoduleWalk = 128

// submoduleFetchTimeout bounds the one fetch a stale pointer is answered with, generously: a store's first
// fetch is a real clone. Only the cancelled context bounds the child — the WaitDelay `git.CommandContext`
// carries bounds its pipes.
const submoduleFetchTimeout = 5 * time.Minute

// submoduleFetchWait is how long a second fetcher of one checkout may queue behind the first. The fetch it
// waits for is itself bounded, so a wait past that bound means the holder is wedged and "another fetch is
// running" is the honest answer rather than a longer wait. A var so a test can hold the lock and watch the
// second arrival give up rather than queue for the real seven minutes.
var submoduleFetchWait = submoduleFetchTimeout + 2*time.Minute

// gitmodulesFile is the file a superproject records its submodules in.
const gitmodulesFile = ".gitmodules"

// maxSubmoduleURLLen bounds the address a `.gitmodules` entry may put in front of a person, on a log line and
// on a command line.
const maxSubmoduleURLLen = 2048

// objectIDRe is a git object id: SHA-1, or SHA-256 in a repository created with `--object-format=sha256`.
var objectIDRe = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// submodule is one gitlink a task's commit names, resolved to the shared store its objects come from.
type submodule struct {
	// path is the submodule directory, workspace-relative (the path the superproject records).
	path string
	// name is that path as the parent's own tree records it, which the fallback derivation uses.
	name string
	// sha is the commit the superproject records for it.
	sha string
	// store is the shared gitdir the person's own checkout populated, found by resolveStore.
	store string
	// dir is the checkout whose `.gitmodules` names this link — the directory git's own verb must run in to
	// reach it, for a nested submodule that is its parent's checkout rather than the project's.
	dir string
	// store_of_parent is the repository whose remote a relative `.gitmodules` entry resolves against.
	parentStore string
	// url is what git reads out of that file for this path, unredacted: redact it before showing it.
	url string
	// configURL is what the checkout's own `.git/config` records for this section. Git dials *that* one
	// whenever it is set and falls back to `.gitmodules` only without it, so it is the address a fetch really
	// contacts, and it is screened beside `url` — the two drift apart the moment the repository moves a
	// submodule and nobody runs `git submodule sync`.
	configURL string
	// note is MIKE's own sentence for why a link is not populated, when it is not.
	note string
}

// PopulateSubmodules gives the worktree every submodule its own commit names, nested ones included, and
// returns those paths parent-first; one already at that commit is left alone. A store the person never
// populated is an error naming it, never an empty directory handed to the agent.
//
// projectPath is the checkout the person populated, whose submodule working trees name the stores this hands
// out; worktreePath is the task's directory the checkouts are built in.
func PopulateSubmodules(ctx context.Context, worktreePath, projectPath string, opts PopulateOpts) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, projectPath, nil, func(s submodule) error {
		if err := ensureCommit(ctx, s, opts.AllowStoreFetch, opts.Fetch); err != nil {
			return fmt.Errorf("submodule %s: %w", s.path, err)
		}
		if err := checkoutSubmodule(s.store, s.sha, filepath.Join(worktreePath, filepath.FromSlash(s.path))); err != nil {
			return fmt.Errorf("submodule %s: %w", s.path, err)
		}
		out = append(out, s.path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SubmodulePaths lists the submodules a worktree's own commit names, in the order PopulateSubmodules walks,
// and creates nothing. Every path that builds a container derives its git-pointer pins from here, so a
// rebuild cannot forget a pin the first create remembered.
func SubmodulePaths(worktreePath, projectPath string) ([]string, error) {
	var out []string
	err := walkSubmodules(worktreePath, projectPath, nil, func(s submodule) error {
		out = append(out, s.path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SubmoduleState is one gitlink a commit names, as the person's own checkout holds it. The row is what the
// person is asked to approve and what the fetch is later asked for, so the two cannot disagree about what is
// missing.
type SubmoduleState struct {
	// Path is the submodule directory, workspace-relative.
	Path string `json:"path"`
	// URL is the redacted address MIKE would contact, empty where none is recorded for the path.
	URL string `json:"url"`
	// Populated says the person's copy is there to be read.
	Populated bool `json:"populated"`
	// Note is MIKE's own sentence for what is wrong, and for why MIKE would refuse to fetch it.
	Note string `json:"note"`
}

// SubmoduleStates lists every gitlink a worktree's own commit names and how the person's checkout holds it,
// creating nothing and fetching nothing. A store that is missing stops the walk from descending into it, so a
// nested submodule below one is listed no further than its parent. The context bounds the DNS lookup the
// address screen makes per row, which is the only reason a listing takes as long as a host takes to answer.
func SubmoduleStates(ctx context.Context, worktreePath, projectPath string, fetch SubmoduleFetch) ([]SubmoduleState, error) {
	var out []SubmoduleState
	err := walkSubmodules(worktreePath, projectPath, func(s submodule) error {
		out = append(out, SubmoduleState{Path: s.path, URL: types.RedactURL(s.url), Populated: false, Note: stateNote(ctx, s, fetch)})
		return nil
	}, func(s submodule) error {
		out = append(out, SubmoduleState{Path: s.path, Populated: true})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CheckoutSubmoduleStates is SubmoduleStates over the person's own checkout rather than a task's worktree: the
// set a fetch may be asked for.
func CheckoutSubmoduleStates(ctx context.Context, projectPath string, fetch SubmoduleFetch) ([]SubmoduleState, error) {
	return SubmoduleStates(ctx, projectPath, projectPath, fetch)
}

// stateNote says why a row is unpopulated, and adds the refusal a fetch would meet so a person is not asked
// to click something that cannot work. The checkout's own recorded address is screened too, and named when it
// differs: showing the person the `.gitmodules` address while git would dial another asks them to approve a
// destination they are not looking at.
func stateNote(ctx context.Context, s submodule, fetch SubmoduleFetch) string {
	note := s.note
	if err := fetch.screenAddresses(ctx, s.parentStore, s.url, s.configURL); err != nil {
		return joinNote(note, err.Error())
	}
	if divergingURL(s.url, s.configURL) {
		return joinNote(note, "the checkout records a different address for it than "+gitmodulesFile+
			" does — run git submodule sync there first")
	}
	return note
}

// joinNote appends one sentence to MIKE's reason, which is what the person reads on the row.
func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "; " + extra
}

// SubmodulesUninitialized stops a task on the submodules its commit names that the person never populated,
// naming for each the address MIKE would contact. It is the task's answer, and the card's notice: the row
// goes to Review so the person can answer it rather than retry it.
type SubmodulesUninitialized struct {
	// Project is the checkout the person populates, named because the fix happens there.
	Project string
	// Submodules holds the unpopulated rows, URLs already redacted.
	Submodules []SubmoduleState
}

func (e *SubmodulesUninitialized) Error() string {
	lines := make([]string, 0, len(e.Submodules))
	for _, s := range e.Submodules {
		line := s.Path
		if s.URL != "" {
			line += " → " + s.URL
		}
		if s.Note != "" {
			line += " (" + s.Note + ")"
		}
		lines = append(lines, line)
	}
	what := "submodules"
	if len(e.Submodules) == 1 {
		what = "submodule"
	}
	return fmt.Sprintf("the project checkout at %s has not fetched %d %s: %s — initialize the project's submodules, then start the task again",
		e.Project, len(e.Submodules), what, strings.Join(lines, "; "))
}

// PopulateOpts is what the person decided about one project's submodules, handed down rather than read here.
type PopulateOpts struct {
	// AllowStoreFetch lets a stale pointer be answered with one fetch into the shared store, over that
	// store's own remote. Its zero value declines, so a caller that forgot to decide cannot reach the
	// network from a task.
	AllowStoreFetch bool
	// Fetch carries the operator's decision, because that fetch dials an address: the store's recorded
	// origin. Zero value screens it with the production rule.
	Fetch SubmoduleFetch
}

// walkSubmodules walks the gitlinks of a worktree's own commit out through each submodule's shared store,
// calling visit parents first. A store the person never populated, or one whose working copy is gone, goes to
// onMissing where one is given; with none it is an error, because skipping a submodule would under-pin the
// container that mounts it.
func walkSubmodules(worktreePath, projectPath string, onMissing func(submodule) error, visit func(submodule) error) error {
	// The checkout's own gitdir holds the `modules` tree it populated: the common directory for an ordinary
	// checkout, the admin directory for a project that is itself a `git worktree`. A checkout that is gone
	// leaves the task's worktree to read the shared objects, and the derivation alone to find stores by.
	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		if gitDir, err = CommonGitDir(worktreePath); err != nil {
			return err
		}
	}
	head, err := commitAt(worktreePath, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve the task worktree's commit: %w", err)
	}

	// A node is one repository's commit, the store its trees are read from, and where its checkout lives
	// relative to the task's worktree.
	type node struct {
		store  string
		sha    string
		prefix string
		depth  int
	}

	count := 0
	walk := []node{{store: gitDir, sha: head}}
	for len(walk) > 0 {
		n := walk[0]
		walk = walk[1:]

		links, err := gitlinks(n.store, n.sha, n.prefix)
		if err != nil {
			return err
		}
		modulesDir := filepath.Join(n.store, submoduleModulesDir)
		// The parent's `.gitmodules` is asked for only when a link is missing, so the populated path — every
		// task of every project that has its copies — never pays for the read.
		var table map[string]string
		for _, link := range links {
			if count++; count > maxSubmoduleWalk {
				return fmt.Errorf("more than %d submodules in this repository", maxSubmoduleWalk)
			}
			// A project moved out from under a live task: no checkout to name, so only the derivation.
			var checkout string
			if projectPath != "" {
				checkout = filepath.Join(projectPath, filepath.FromSlash(link.path))
			}
			link.dir = parentCheckout(projectPath, n.store, n.prefix)
			link.parentStore = n.store
			store := resolveStore(modulesDir, checkout, link.name)
			if store == "" || !storeWorktreeHolds(store) {
				if onMissing == nil {
					if store == "" {
						return fmt.Errorf("submodule %s has no local copy under %s: populate it in the project checkout at %s (git submodule update --init --recursive %s) before starting a task",
							link.path, modulesDir, projectPath, link.path)
					}
					return fmt.Errorf("submodule %s's local copy at %s has no working tree — %s is gone: restore it in the project checkout at %s (git submodule update --init --recursive %s) before starting a task",
						link.path, store, worktreeOf(store), projectPath, link.path)
				}
				if table == nil {
					table = submoduleTable(link.dir)
				}
				link.store = store
				link.url = table[link.name]
				link.configURL = submoduleConfigURL(link.dir, link.name)
				link.note = missingNote(store)
				if err := onMissing(link); err != nil {
					return err
				}
				// Nothing to walk below a copy that is not there.
				continue
			}
			link.store = store
			if err := visit(link); err != nil {
				return err
			}
			if n.depth+1 >= maxSubmoduleWalk {
				logging.For(logging.Git).Warnf("stopped walking submodules at %s: depth limit %d reached", link.path, maxSubmoduleWalk)
				continue
			}
			walk = append(walk, node{store: store, sha: link.sha, prefix: link.path, depth: n.depth + 1})
		}
	}
	return nil
}

// resolveStore names the shared gitdir under modulesDir that holds the person's copy of the submodule whose
// working tree is checkout, or "" where they never populated one. git keys that directory by the
// `.gitmodules` section rather than by the path, so the checkout's own answer is asked first and the
// `<modulesDir>/<name>` derivation is the fallback for a store whose checkout is gone.
func resolveStore(modulesDir, checkout, name string) string {
	if asked := storeOfCheckout(checkout); asked != "" {
		// Only a store inside this checkout's own `modules` tree counts: that tree is what the container pins
		// read-only. An answer naming metadata elsewhere on the disk is another repository's.
		if dir, err := fsutil.Contained(asked, []string{modulesDir}); err == nil {
			return dir
		}
	}
	if derived := filepath.Join(modulesDir, filepath.FromSlash(name)); isGitDir(derived) {
		return derived
	}
	return ""
}

// storeOfCheckout is the gitdir the checkout at dir uses, or "" where there is no checkout to ask. The git
// pointer is looked for before git is, because rev-parse walks up: an unpopulated submodule, or an empty path,
// would otherwise answer with somebody else's repository.
func storeOfCheckout(dir string) string {
	if dir == "" || !HasGitDir(dir) {
		return ""
	}
	asked, err := absGitDir(dir, "--git-dir")
	if err != nil {
		return ""
	}
	return asked
}

// worktreeOf is the working tree a store serves, or "" where its configuration names none — the form a bare
// repository is left in. The store's own `config` is read by name because git changes into the working tree
// before it runs a single command, so a store whose copy is gone cannot be opened as a repository at all.
func worktreeOf(store string) string {
	out, err := Run("", "config", "--file", filepath.Join(store, "config"), "--get", "core.worktree")
	if err != nil {
		return ""
	}
	work := strings.TrimSpace(out)
	if work == "" {
		return ""
	}
	if !filepath.IsAbs(work) {
		work = filepath.Join(store, work)
	}
	return filepath.Clean(work)
}

// parentCheckout is where the person's copy of the repository at prefix stands, which is the directory whose
// `.gitmodules` names these links and whose working directory git's submodule verb must be run in. A project
// moved out from under a live task leaves the store's own configuration to name it.
func parentCheckout(projectPath, store, prefix string) string {
	if projectPath != "" {
		return filepath.Join(projectPath, filepath.FromSlash(prefix))
	}
	return worktreeOf(store)
}

// storeWorktreeHolds reports whether the store's registered working tree is present. A store that names none
// is a bare repository, which is a state MIKE reads objects from rather than refuses.
func storeWorktreeHolds(store string) bool {
	work := worktreeOf(store)
	if work == "" {
		return true
	}
	_, err := os.Stat(work)
	return err == nil
}

// missingNote is MIKE's own sentence for why a gitlink has no copy to read.
func missingNote(store string) string {
	if store == "" {
		return "the project checkout never fetched it"
	}
	return fmt.Sprintf("its working copy at %s is gone", worktreeOf(store))
}

// submoduleConfigURL is what the checkout's own `.git/config` records for one submodule section — the address
// git dials whenever it is set. Read through git rather than by parsing, and asked of the checkout rather than
// of `.gitmodules`, because the two are allowed to disagree and git keeps the checkout's answer.
func submoduleConfigURL(checkout, name string) string {
	if checkout == "" || name == "" {
		return ""
	}
	out, err := Run(checkout, "config", "--get", "--null", "submodule."+name+".url")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(out, "\x00")
}

// divergingURL reports whether two readings of one submodule's address name different places. An empty side
// is not a disagreement: it means nobody recorded that reading at all.
func divergingURL(gitmodulesURL, configURL string) bool {
	return gitmodulesURL != "" && configURL != "" && gitmodulesURL != configURL
}

// submoduleTable reads a checkout's submodule entries through git's own config reader, keyed by the path each
// one declares. Git keys a store by its section name rather than by its path, so the only honest way to ask
// what URL is recorded for a path is git's own table, matched on its `.path` value.
func submoduleTable(checkout string) map[string]string {
	if checkout == "" {
		return nil
	}
	out, err := Run("", "config", "--file", filepath.Join(checkout, gitmodulesFile), "--list", "--null")
	if err != nil {
		return nil
	}
	// --null terminates each pair, not each half of it: a field is `key\nvalue`, which is what lets a
	// section name hold a newline or a space.
	var paths, urls = map[string]string{}, map[string]string{}
	for _, field := range strings.Split(out, "\x00") {
		key, value, ok := strings.Cut(field, "\n")
		if !ok {
			continue
		}
		name, field, ok := splitSubmoduleKey(key)
		if !ok {
			continue
		}
		switch field {
		case "path":
			paths[name] = strings.TrimSuffix(value, "/")
		case "url":
			urls[name] = value
		}
	}
	table := make(map[string]string, len(paths))
	for name, p := range paths {
		if u, ok := urls[name]; ok {
			table[path.Clean(filepath.ToSlash(p))] = u
		}
	}
	return table
}

// splitSubmoduleKey cuts `submodule.<name>.<field>` into its name and field. The name may hold dots and, with
// --null, a newline; the field holds neither.
func splitSubmoduleKey(key string) (name, field string, ok bool) {
	const section = "submodule."
	if !strings.HasPrefix(key, section) {
		return "", "", false
	}
	rest := key[len(section):]
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// AllowPrivateSubmoduleHostsEnv is the operator's variable widening the submodule address screen to private
// networks; its name is spelled here as well as in startup because a refusal names it.
const AllowPrivateSubmoduleHostsEnv = "MIKE_ALLOW_PRIVATE_SUBMODULE_HOSTS"

// submoduleDNSLookupTimeout bounds the lookup the address screen makes before git is handed a host. A host
// that cannot be answered is refused rather than handed over unchecked, and git would have failed on it too.
const submoduleDNSLookupTimeout = 5 * time.Second

// screenSubmoduleURL refuses the addresses a repository-chosen text must not make the server dial. A
// submodule's URL is the one remote MIKE contacts on a repository's say-so, and the fetch runs in the server
// process, outside every sandbox — so the address is checked as written and again as every address it resolves
// to, which is what stops a public name standing in for this machine's own services or the cloud's metadata
// address. Widening reaches the private ranges a self-hosted server may live in, and never these.
func screenSubmoduleURL(ctx context.Context, parentStore, raw string, allowPrivateHosts bool) error {
	if raw == "" {
		return fmt.Errorf("no URL is recorded for this path in %s", gitmodulesFile)
	}
	if len(raw) > maxSubmoduleURLLen {
		return fmt.Errorf("its URL is longer than %d characters", maxSubmoduleURLLen)
	}
	address, err := submoduleAddress(parentStore, raw)
	if err != nil {
		return err
	}
	host := address.Hostname()
	if host == "" {
		return fmt.Errorf("its URL names no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return screenAddress(ip, host, allowPrivateHosts)
	}
	lookup, cancel := context.WithTimeout(ctx, submoduleDNSLookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lookup, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("its host %q does not resolve", host)
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			return fmt.Errorf("its host %q resolves to something that is not an address", host)
		}
		if err := screenAddress(ip, host, allowPrivateHosts); err != nil {
			return err
		}
	}
	return nil
}

// submoduleAddress is the address git would dial for a `.gitmodules` entry, in the form MIKE can screen. A
// relative entry is resolved against the remote of the repository that records it, the way git resolves it, and
// a scheme-less scp-style entry is read as SSH. What is left is a path on this machine, and a submodule is
// never one of those.
func submoduleAddress(parentStore, raw string) (*url.URL, error) {
	text := strings.TrimSpace(raw)
	switch {
	case text == "" || strings.HasPrefix(text, "-"):
		return nil, fmt.Errorf("its URL is empty or starts with a dash")
	case strings.ContainsAny(text, "\x00\n\r\v\f \t"):
		return nil, fmt.Errorf("its URL contains whitespace or a control character")
	case !strings.Contains(text, "://"):
		switch {
		case strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../"):
			resolved, err := resolveRelativeRemote(remoteOf(parentStore), text)
			if err != nil {
				return nil, err
			}
			text = resolved
		case strings.Contains(text, ":") && !strings.HasPrefix(text, "/"):
			// scp-style `user@host:path`: the colon after the host is a separator, not a port, so it
			// becomes the slash git would put there. A doubled colon is the remote-helper syntax.
			host, rest, _ := strings.Cut(text, ":")
			if host == "" || strings.HasPrefix(rest, ":") {
				return nil, fmt.Errorf("its URL is not an address git could dial")
			}
			if !strings.HasPrefix(rest, "/") {
				rest = "/" + rest
			}
			text = "ssh://" + host + rest
		default:
			return nil, fmt.Errorf("its URL is a path on this machine, not a remote")
		}
	}
	address, err := url.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("its URL does not parse: %s", err)
	}
	if !slices.Contains(submoduleProtocols, address.Scheme) {
		return nil, fmt.Errorf("its URL uses the %q transport, which a submodule may not", address.Scheme)
	}
	if address.Hostname() == "" {
		return nil, fmt.Errorf("its URL names no host")
	}
	return address, nil
}

// resolveRelativeRemote is git's rule for a `../other` submodule URL: the address of the repository above it,
// with the entry resolved against the directory holding that repository.
func resolveRelativeRemote(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("its URL is relative and the repository above it has no remote to resolve it against")
	}
	if !strings.Contains(base, "://") && strings.Contains(base, ":") && !strings.HasPrefix(base, "/") {
		base = "ssh://" + base
	}
	address, err := url.Parse(base)
	if err != nil || address.Host == "" {
		return "", fmt.Errorf("the remote of the repository above it, %s, is no address to resolve against", types.RedactURL(base))
	}
	address.Path = path.Join(path.Dir(strings.TrimSuffix(address.Path, "/")), rel)
	address.RawQuery, address.Fragment = "", ""
	return address.String(), nil
}

// screenAddress is the address rule itself, run on the written form and on every form the name resolves to.
func screenAddress(ip net.IP, host string, allowPrivateHosts bool) error {
	switch {
	case ip.IsLoopback(), ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("its host %s is %s, an address on this machine or the local network", host, ip)
	case !allowPrivateHosts && ip.IsPrivate():
		return fmt.Errorf("its host %s is the private address %s; set %s=1 to allow a self-hosted server on a private network",
			host, ip, AllowPrivateSubmoduleHostsEnv)
	}
	return nil
}

// gitlinks lists the gitlinks of one commit as submodule rows, NUL-delimited so a path with a quote or a
// newline arrives whole. The listing comes from the commit, not the working tree, so what an agent writes
// later cannot choose which paths a container mounts.
func gitlinks(store, sha, prefix string) ([]submodule, error) {
	out, err := Run(store, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, fmt.Errorf("list submodules of %s: %s", shortHash(sha), gitMessage(out, err))
	}
	var links []submodule
	for _, entry := range strings.Split(out, "\x00") {
		// "<mode> <type> <sha>\t<path>"
		fields := strings.SplitN(entry, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		meta := strings.Fields(fields[0])
		if len(meta) != 3 || meta[0] != "160000" {
			continue
		}
		if err := validateSubmodulePath(fields[1]); err != nil {
			return nil, err
		}
		links = append(links, submodule{
			path: path.Join(prefix, fields[1]),
			name: fields[1],
			sha:  meta[2],
		})
	}
	return links, nil
}

// validateSubmodulePath rejects a gitlink path that names a directory outside the worktree or would take a
// git pointer's place, which is where it becomes a filesystem path and a mount point.
func validateSubmodulePath(p string) error {
	if err := fsutil.ValidateRelPath(p); err != nil {
		return fmt.Errorf("submodule path %q: %w", p, err)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".git" {
			return fmt.Errorf("submodule path %q would replace a git pointer", p)
		}
	}
	return nil
}

// checkoutSubmodule gives dst the recorded commit as a linked worktree of the submodule's own shared
// repository. A checkout already at that commit is left alone: touching it would pull a mount out from under
// a container that may have it mounted.
func checkoutSubmodule(store, sha, dst string) error {
	if current := checkedOutCommit(dst); current == sha {
		return nil
	} else if current != "" {
		// The task's own earlier turn moved it: the directory is the task's, and the superproject cannot see
		// through the gitlink.
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("clear the stale submodule checkout: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if out, err := Run(store, "worktree", "add", "--detach", dst, sha); err != nil {
		return fmt.Errorf("create the submodule worktree: %s: %w", gitMessage(out, err), err)
	}
	return nil
}

// checkedOutCommit is the commit the checkout at dir stands on, or "" where there is no checkout.
func checkedOutCommit(dir string) string {
	if !HasGitDir(dir) {
		return ""
	}
	out, _ := Run(dir, "rev-parse", "--quiet", "HEAD")
	return strings.TrimSpace(out)
}

// ensureCommit makes the shared store hold a commit, fetching once into the store — never into a task — when
// it does not and the project allows a task that fetch, so the next task on that pointer is local again.
//
// The store's own remote is what this dials, and it is bytes in a config file MIKE did not write: the person
// populates these checkouts, and any earlier MIKE or any `git remote set-url` left an address there. It is
// therefore screened like the others, and fetched over the submodule transport rather than the ordinary one,
// so the same protocol allowlist and the same refusal of a redirect cover it.
func ensureCommit(ctx context.Context, s submodule, allowFetch bool, fetch SubmoduleFetch) error {
	if !objectIDRe.MatchString(s.sha) {
		return fmt.Errorf("submodule records %q, which is not a commit id", s.sha)
	}
	if _, err := Run(s.store, "cat-file", "-e", s.sha+"^{commit}"); err == nil {
		return nil
	}
	if !allowFetch {
		return fmt.Errorf("the local copy has no commit %s and this project does not let a task reach the network for one: fetch it in the project checkout (git -C %s fetch origin), or allow network for the project",
			shortHash(s.sha), s.store)
	}
	origin := remoteOf(s.store)
	if origin == "" {
		return fmt.Errorf("its store has no remote to fetch %s from", shortHash(s.sha))
	}
	if err := fetch.screenAddresses(ctx, s.parentStore, origin, s.configURL); err != nil {
		return fmt.Errorf("its remote would not be reached: %w", err)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, submoduleFetchTimeout)
	defer cancel()
	cmd := CommandContext(fetchCtx, s.store, "fetch", "--no-tags", "origin")
	cmd.Env = submoduleEnv(fetch.widen)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch %s for commit %s: %s: %w", remoteURL(s.store), shortHash(s.sha), gitMessage(string(out), err), err)
	}
	if _, err := Run(s.store, "cat-file", "-e", s.sha+"^{commit}"); err != nil {
		return fmt.Errorf("remote %s does not have commit %s", remoteURL(s.store), shortHash(s.sha))
	}
	logging.For(logging.Git).Infof("fetched a submodule's missing commit into the shared store at %s", s.store)
	return nil
}

// remoteURL names the store's origin, for a failure a person has to act on.
func remoteURL(store string) string {
	if u := remoteOf(store); u != "" {
		return u
	}
	return "origin (no URL configured)"
}

// remoteOf is the store's origin, or "" where there is no store to ask or none is configured. Asking git with
// no directory would answer with whatever remote the server's own working directory holds.
func remoteOf(store string) string {
	if store == "" {
		return ""
	}
	out, err := Run(store, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// checkoutState is what the person's own submodule checkout holds, which decides whether fetching into it
// can cost them anything.
type checkoutState int

const (
	// checkoutAbsent is the state a fresh clone and `git worktree add` leave: no working copy at all.
	checkoutAbsent checkoutState = iota
	// checkoutAtCommit holds the recorded commit and nothing of the person's own on top of it.
	checkoutAtCommit
	checkoutDirty
	checkoutElsewhere
	// checkoutNotASubmodule is content with no git pointer, so not git's copy to move.
	checkoutNotASubmodule
)

func (s checkoutState) String() string {
	switch s {
	case checkoutDirty:
		return "it holds uncommitted work of the person's own"
	case checkoutElsewhere:
		return "it stands on a commit of the person's own, not the one the superproject records"
	case checkoutNotASubmodule:
		return "its directory holds files with no git pointer in it"
	}
	return "its working copy is missing"
}

// SubmoduleFetch is what the operator and the person decided about reaching the network for a submodule. Its
// zero value asks for nothing that was not already there, so a caller that never decided is refused rather
// than left to default outward.
type SubmoduleFetch struct {
	// AllowPrivateHosts widens the address screen to the operator's own networks. See screenSubmoduleURL:
	// the loopback, link-local and unspecified addresses stay refused with it set.
	AllowPrivateHosts bool
	// screen replaces the address rule, and widen appends environment to the transport fence. Both belong to
	// a fixture whose upstream is a directory on this machine, which the address rule refuses and the fence
	// will not dial; both are unexported so no call site can put a submodule fetch outside the screen. A
	// zero value means the production rule runs.
	screen func(url string) error
	widen  []string
}

// screenFor is the address rule to run one entry's URL against, given the repository that records it: a
// relative entry is resolved against that repository's remote before it is judged. The context bounds the
// hostname lookup, which is the one duration about a listing nobody can predict.
func (f SubmoduleFetch) screenFor(ctx context.Context, parentStore string) func(string) error {
	if f.screen != nil {
		return f.screen
	}
	return func(raw string) error { return screenSubmoduleURL(ctx, parentStore, raw, f.AllowPrivateHosts) }
}

// screenAddresses screens every address git may dial for one submodule, and returns the first refusal.
//
// One URL is not enough because git does not use just one: it dials the checkout's own recorded address and
// falls back to `.gitmodules` only where that is unset, so a repository that moved a submodule leaves the
// checkout dialling the old address while the new one is what gets shown and screened. Every reading is
// therefore judged, and a refusal of any of them stops the fetch. `required` is judged even when empty, which
// is how "no address is recorded for this path" reaches the person.
func (f SubmoduleFetch) screenAddresses(ctx context.Context, parentStore, required string, extra ...string) error {
	screen := f.screenFor(ctx, parentStore)
	if err := screen(required); err != nil {
		return err
	}
	for _, u := range extra {
		if u == "" {
			continue
		}
		if err := screen(u); err != nil {
			return err
		}
	}
	return nil
}

// FetchSubmodules fetches, into the person's checkout, the named submodules where that checkout's own commit
// records them, and returns the paths it populated.
func FetchSubmodules(ctx context.Context, projectPath string, paths []string, fetch SubmoduleFetch, onStatus func(string)) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	return fetchRounds(ctx, projectPath, fetch, onStatus, nil, func(s submodule) bool {
		return wantedSubmodule(paths, s.path)
	})
}

// fetchRounds brings the named submodules down and then, round by round, whatever their copies reveal
// underneath them, all under one lock on the checkout.
//
// Levels are asked for one at a time rather than with git's `--recursive`, because a nested `.gitmodules` is
// text that arrives inside a clone: MIKE has read no line of it, so no screen has run on the addresses it
// names. Re-listing between rounds makes every contacted address one MIKE enumerated and judged. `first` is
// the round-0 listing where the caller already holds one; a row is asked for once, so a refused address or an
// unreachable remote is reported once rather than on every round.
func fetchRounds(ctx context.Context, projectPath string, fetch SubmoduleFetch, onStatus func(string), first []submodule, want func(submodule) bool) ([]string, error) {
	release, err := lockSubmoduleFetch(projectPath)
	if err != nil {
		return nil, err
	}
	defer release()

	var populated []string
	asked := map[string]bool{}
	for round := 0; round < maxSubmoduleWalk; round++ {
		links := first
		if round > 0 || links == nil {
			if links, err = missingLinks(projectPath); err != nil {
				return populated, err
			}
		}
		var refs []submodule
		for _, link := range links {
			if asked[link.path] || !want(link) {
				continue
			}
			asked[link.path] = true
			refs = append(refs, link)
		}
		if len(refs) == 0 {
			return populated, nil
		}
		got, err := initCheckouts(ctx, projectPath, refs, fetch, onStatus)
		populated = append(populated, got...)
		if err != nil {
			return populated, err
		}
	}
	return populated, fmt.Errorf("more than %d rounds of submodules under %s", maxSubmoduleWalk, projectPath)
}

// missingLinks is what the person's own checkout should hold and does not, as the rows a fetch needs.
func missingLinks(projectPath string) ([]submodule, error) {
	var out []submodule
	err := walkSubmodules(projectPath, projectPath, func(s submodule) error {
		out = append(out, s)
		return nil
	}, func(submodule) error { return nil })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// wantedSubmodule asks whether a path is one the caller named or sits under one. Naming a submodule brings its
// nested ones, which only become listable once it is fetched — the levels below a copy nobody downloaded are
// still the same submodule's tree, not something a stranger asked for.
func wantedSubmodule(paths []string, p string) bool {
	for _, w := range paths {
		if p == w || strings.HasPrefix(p, w+"/") {
			return true
		}
	}
	return false
}

// InitSubmodules fetches, into the person's own checkout, the working copy of every submodule that checkout's
// commit names, nested ones included, and returns the paths it populated. It belongs to project import and to
// the button that re-runs it: the fetch spends the operator's own credentials (`credential.helper`, their ssh
// agent) on a destination the repository's text chose, and leaves that destination as the store's `origin`,
// which every later fetch then treats as the person's own remote.
//
// A checkout the person has content in is left alone: `git submodule update` checks the recorded commit out,
// which detaches their own submodule off their work. onStatus, which may be nil, hears every outcome worth
// reporting; a submodule that will not fetch is skipped, because a project whose upstream is unreachable is
// still a project.
func InitSubmodules(ctx context.Context, projectPath string, fetch SubmoduleFetch, onStatus func(string)) ([]string, error) {
	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		return nil, err
	}
	head, err := commitAt(projectPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve the project's commit: %w", err)
	}
	// The listing comes from the commit, so a `.gitmodules` advertising a submodule no commit names is never
	// fetched, and every path handed to git is one validateSubmodulePath already accepted. Asking for the
	// whole commit's gitlinks rather than only the missing ones is what lets a checkout of the person's own
	// be reported as left alone.
	links, err := gitlinks(gitDir, head, "")
	if err != nil {
		return nil, err
	}
	for i := range links {
		links[i].dir = projectPath
		links[i].parentStore = gitDir
	}
	return fetchRounds(ctx, projectPath, fetch, onStatus, links, func(submodule) bool { return true })
}

// initCheckouts runs git's own verb for each named link, in the checkout that records it, and reports every
// outcome worth a person's attention. The caller holds the project's fetch lock, because a round-by-round
// fetch must not interleave with another fetcher's.
func initCheckouts(ctx context.Context, projectPath string, links []submodule, fetch SubmoduleFetch, onStatus func(string)) ([]string, error) {
	if len(links) == 0 {
		return nil, nil
	}
	if len(links) > maxSubmoduleWalk {
		return nil, fmt.Errorf("more than %d submodules in this repository", maxSubmoduleWalk)
	}
	report := submoduleReporter(projectPath, onStatus)

	var populated []string
	for _, link := range links {
		checkout := filepath.Join(link.dir, filepath.FromSlash(link.name))
		switch state := checkoutStateOf(checkout, link.sha); state {
		case checkoutAtCommit:
			populated = append(populated, link.path)
			continue
		case checkoutAbsent:
		default:
			report("submodule %s was left alone: %s", link.path, state)
			continue
		}
		if err := fetch.screenAddresses(ctx, link.parentStore, submoduleTable(link.dir)[link.name], submoduleConfigURL(link.dir, link.name)); err != nil {
			report("submodule %s was refused: %s", link.path, err)
			continue
		}
		cmd := CommandContext(ctx, link.dir, submoduleInitArgs(link)...)
		cmd.Env = submoduleEnv(fetch.widen)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// A cancelled request is the caller's own navigation, not a repository that could not be
			// fetched; the same distinction the clone path makes.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return populated, ctxErr
			}
			report("submodule %s was not fetched: %s", link.path, gitMessage(string(out), err))
			continue
		}
		// The origin git recorded is what was contacted — the address the screen ran against came out of the
		// same file, and this is the reading that says git ended up where that address said.
		report("submodule %s was fetched from %s", link.path, types.RedactURL(contactedRemote(link, checkout)))
		populated = append(populated, link.path)
	}
	return populated, nil
}

// submoduleInitArgs is the argv that fetches one submodule's working copy: git's own verb, for one validated
// path the parent's commit records rather than for every section `.gitmodules` advertises, run in the checkout
// that records it. Never `--recursive`: see fetchRounds.
func submoduleInitArgs(s submodule) []string {
	return []string{"submodule", "update", "--init", "--", s.name}
}

// submoduleReporter sends every outcome a person should see to whoever asked for the fetch, and to the log as
// well: a fetch that quietly refused an address must be findable afterwards.
func submoduleReporter(projectPath string, onStatus func(string)) func(string, ...any) {
	return func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if onStatus != nil {
			onStatus(line)
		}
		logging.For(logging.Git).Warnf("fetching submodules of %s: %s", projectPath, line)
	}
}

// submoduleFetchLocks is the domain keeping one checkout's submodule fetch single-threaded, because two
// `submodule update --init` in one checkout write its `.git/config` and its `modules` tree together.
type submoduleFetchLocks struct{ keylock.Table }

// submoduleFetchLock is the one fetcher of a checkout. Every fetch arrives through lockSubmoduleFetch.
var submoduleFetchLock submoduleFetchLocks

// lockSubmoduleFetch takes the checkout's fetch lock or reports the fetch already running. The wait is bounded
// because the fetch it waits for is itself bounded — a holder still there past that is wedged, and "another
// fetch is running" is the true answer.
func lockSubmoduleFetch(projectPath string) (func(), error) {
	release, ok := submoduleFetchLock.TryLock(projectPath, submoduleFetchWait)
	if !ok {
		return nil, fmt.Errorf("another submodule fetch is already running for this project")
	}
	return release, nil
}

// contactedRemote is the address the fetched store names as its origin — what git really dialed, which the
// report and the log carry redacted.
func contactedRemote(s submodule, checkout string) string {
	store := s.store
	if store == "" {
		store = resolveStore(filepath.Join(s.parentStore, submoduleModulesDir), checkout, s.name)
	}
	return remoteURL(store)
}

// checkoutStateOf is what the person's own working copy of a submodule holds. The directory is read before git
// is asked anything, because a never-populated submodule leaves an empty one.
func checkoutStateOf(checkout, sha string) checkoutState {
	entries, err := os.ReadDir(checkout)
	if err != nil || len(entries) == 0 {
		return checkoutAbsent
	}
	if !HasGitDir(checkout) {
		return checkoutNotASubmodule
	}
	if checkedOutCommit(checkout) != sha {
		return checkoutElsewhere
	}
	if out, err := Run(checkout, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		return checkoutDirty
	}
	return checkoutAtCommit
}

// PruneSubmoduleWorktrees prunes every submodule store's own worktree registry under a project, the twin of the
// project's own prune. Each store is swept by git, never by removing registrations from an admin directory
// whose name git derives from a checkout that may be gone.
func PruneSubmoduleWorktrees(projectPath string) {
	gitDir, err := checkoutGitDir(projectPath)
	if err != nil {
		return
	}
	for _, store := range submoduleStores(filepath.Join(gitDir, submoduleModulesDir), 0) {
		if _, err := Run(store, "worktree", "prune"); err != nil {
			logging.For(logging.Git).Warnf("pruning submodule worktrees in %s failed", store)
		}
	}
}

// submoduleStores collects every submodule gitdir under dir. The segments of a nested path are plain
// directories on the way to the gitdir at the end of them, and a nested submodule hangs off its parent
// store's own `modules`.
func submoduleStores(dir string, depth int) []string {
	if depth >= maxSubmoduleWalk {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var stores []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if !isGitDir(child) {
			// A path segment on the way to a store, e.g. the `vendor` of `vendor/lib`.
			if !slices.Contains(gitDirInternals, entry.Name()) {
				stores = append(stores, submoduleStores(child, depth+1)...)
			}
			continue
		}
		stores = append(stores, child)
		stores = append(stores, submoduleStores(filepath.Join(child, submoduleModulesDir), depth+1)...)
	}
	return stores
}

// gitDirInternals are the directories a git repository keeps inside its own gitdir, none of which holds a
// submodule store — `worktrees` holds the registrations this sweep is about.
var gitDirInternals = []string{
	"branches", "hooks", "info", "logs", "objects", "refs", "rr-cache", "svn", "worktrees",
}

// isGitDir reports whether dir is a repository's gitdir rather than a linked worktree's admin directory:
// both carry a HEAD, and only the admin directory carries a commondir pointing back at the repository.
func isGitDir(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, "HEAD")); err != nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, "commondir")); err == nil {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, "objects"))
	return err == nil
}

// commitAt resolves a ref to a commit id and returns it only in the form git itself would accept as one,
// so a ref read back from a repository is never pasted on to another command line.
func commitAt(dir, ref string) (string, error) {
	out, err := Run(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s is not a commit: %s", ref, gitMessage(out, err))
	}
	sha := strings.TrimSpace(out)
	if !objectIDRe.MatchString(sha) {
		return "", fmt.Errorf("%s resolved to %q, which is not a commit id", ref, sha)
	}
	return sha, nil
}
