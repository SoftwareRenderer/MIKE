package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mike/backend/errs"
	"mike/backend/fsutil"
	"mike/backend/sandbox"
	"mike/backend/types"
	"mike/pkg/logging"
)

// Preview document lifecycle limits.
const (
	previewTTL         = 10 * time.Minute // idle before GC; every read refreshes
	previewMaxLive     = 32               // live doc cap; oldest evicted
	previewMaxContent  = 10 << 20         // doc size cap
	previewMaxAsset    = 25 << 20         // sibling asset size cap
	previewSweepPeriod = 30 * time.Second // TTL sweep interval
	// previewMaxAge is a hard limit: every read refreshes the idle TTL, and a read costs only the URL,
	// so a leaked token would otherwise live as long as whoever holds it keeps asking for it.
	previewMaxAge = 60 * time.Minute
)

// Every preview policy ends in `sandbox`: this URL is on the application's own origin, so a document
// here could otherwise read the session cookie and the CSRF token. The frame's own sandbox attribute is
// the second lock, not the first.
const previewDocCSPStrict = "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' data: blob:; style-src 'self' 'unsafe-inline' data: blob:; img-src 'self' data: blob:; font-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'self' blob: data:; object-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts"

// previewDocCSPNoScript — the default posture: nothing runs and nothing is sent out, while the page's
// own static resources still load. It is its own policy because the strict one permits script execution,
// which a frame with a dropped sandbox attribute, or this URL opened in a tab, would then use.
const previewDocCSPNoScript = "default-src 'none'; script-src 'none'; worker-src 'none'; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; style-src * 'unsafe-inline' data: blob:; img-src * data: blob:; font-src * data: blob:; media-src * data: blob:; frame-src * data: blob:; sandbox;"

// previewDocCSPNetwork — the permissive policy, reached only through the preview.serveNetwork grant.
// Its `connect-src *` includes 'self', so this is the policy that most needs the sandbox.
const previewDocCSPNetwork = "default-src * data: blob: 'unsafe-inline'; script-src * 'unsafe-inline' data: blob:; style-src * 'unsafe-inline' data: blob:; img-src * data: blob:; font-src * data: blob:; connect-src * data: blob:; frame-src * data: blob:; worker-src * blob: data:; object-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts"

// previewNavRecord is the last committed HTML-family page load under a token. A failed or out-of-tree
// load records nothing, so the extension's watchdog stops any navigation that produced no fresh record.
type previewNavRecord struct {
	path  string // token-relative path ("" = the doc's own dir)
	isDoc bool   // the doc's own content (dir URL or self-reference)
}

// previewPosture is what a served preview document may do; one policy per posture.
type previewPosture int

const (
	// previewNoScript is the zero value, so anything unrecognised answers with it.
	previewNoScript previewPosture = iota
	// previewScripts runs the page's JavaScript under the strict zero-egress policy.
	previewScripts
	// previewNetwork runs scripts and lets the page reach the network.
	previewNetwork
)

// The wire names of the postures, as the host posts them.
const (
	previewPostureNoScriptName = "no-script"
	previewPostureScriptsName  = "scripts"
	previewPostureNetworkName  = "network"
)

// previewPostureFrom maps the posture name the host posts to a policy. The name is the extension's own
// word and not a grant, so anything unrecognised is previewNoScript; the manifest grant in
// frontend/src/lib/extensions/channel.ts is what limits the names an extension may post.
func previewPostureFrom(name string) previewPosture {
	switch name {
	case previewPostureScriptsName:
		return previewScripts
	case previewPostureNetworkName:
		return previewNetwork
	default:
		return previewNoScript
	}
}

// previewDoc is a served preview document.
type previewDoc struct {
	content    string
	fileName   string // previewed file's base name ("" if unknown)
	baseDir    string // worktree root, or the file's own dir when outside it ("" = self-contained)
	dirRel     string // doc's dir relative to baseDir ("" = baseDir itself)
	posture    previewPosture
	createdAt  time.Time
	expired    bool // past previewMaxAge: a stub that only answers "preview expired"
	navSeq     int  // committed page loads under this token; the watchdog compares it to detect a fresh load
	lastNav    previewNavRecord
	lastAccess time.Time
}

// previewDocContentType: natural type of the previewed file (host-derived —
// extensions can't influence it), defaulting to text/html.
func previewDocContentType(fileName string) string {
	ct := ""
	if fileName != "" {
		ct = mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	}
	if ct == "" {
		ct = "text/html"
	}
	// The platform mime database may already carry a charset parameter.
	if strings.HasPrefix(ct, "text/html") && !strings.Contains(ct, "charset=") {
		ct += "; charset=utf-8"
	}
	return ct
}

// previewStore holds live preview documents keyed by bearer token.
type previewStore struct {
	mu   sync.Mutex
	docs map[string]*previewDoc
	stop chan struct{}
}

func newPreviewStore() *previewStore {
	return &previewStore{docs: make(map[string]*previewDoc), stop: make(chan struct{})}
}

func (ps *previewStore) startSweeper() {
	go func() {
		ticker := time.NewTicker(previewSweepPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ps.stop:
				return
			case <-ticker.C:
				ps.sweep(time.Now())
			}
		}
	}()
}

func (ps *previewStore) stopSweeper() {
	select {
	case <-ps.stop:
	default:
		close(ps.stop)
	}
}

// errPreviewTooLarge: the stored (post-rewrite) content exceeds the cap.
var errPreviewTooLarge = errors.New("preview content too large")

func (ps *previewStore) put(content, fileName, baseDir, dirRel string, posture previewPosture) (string, error) {
	// Mint the token first — the rewrite target is built from it. A mint failure
	// aborts the put, so a weak token is never registered or served.
	token, err := newPreviewToken()
	if err != nil {
		return "", err
	}
	if isHTMLFamilyCT(previewDocContentType(fileName)) {
		content = rewriteRootRelative(content, "/api/preview/"+token+"/")
	}
	if len(content) > previewMaxContent {
		return "", errPreviewTooLarge
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for len(ps.docs) >= previewMaxLive {
		ps.evictOldestLocked()
	}
	now := time.Now()
	ps.docs[token] = &previewDoc{content: content, fileName: fileName, baseDir: baseDir, dirRel: dirRel, posture: posture, lastAccess: now, createdAt: now}
	return token, nil
}

// isHTMLFamilyCT — content types the browser renders as pages; only these
// get the root-relative rewrite and the doc CSP on sibling responses.
func isHTMLFamilyCT(ct string) bool {
	return strings.HasPrefix(ct, "text/html") ||
		ct == "image/svg+xml" ||
		ct == "application/xhtml+xml"
}

func previewDocCSP(doc *previewDoc) string {
	switch doc.posture {
	case previewScripts:
		return previewDocCSPStrict
	case previewNetwork:
		return previewDocCSPNetwork
	default:
		return previewDocCSPNoScript
	}
}

var (
	// Root-relative href/src/action/poster/content="/…". Groups: prefix
	// through the opening quote, the "/", and the rest of the ref (the closing
	// quote is not consumed, so the substitution leaves it in place).
	rootRelAttrRe = regexp.MustCompile(`(\b(?:href|src|action|poster|content)\s*=\s*["'])(/)([^"']*)`)
	// CSS url(/…), quote optional; group 3 stops before ) or a quote.
	rootRelURLRe = regexp.MustCompile(`(url\(\s*["']?)(/)([^)"']*)`)
)

// rewriteRootRelative — the browser resolves "/x" against the origin (the
// host app), but the preview site starts at the worktree, served under the
// token. Absolute and protocol-relative refs are left alone.
func rewriteRootRelative(content, root string) string {
	content = rewriteRootRel(content, root, rootRelAttrRe)
	return rewriteRootRel(content, root, rootRelURLRe)
}

// rewriteRootRel — RE2 has no backreferences, so the substitution rebuilds
// each match from its groups: prefix (group 1) + token root + ref remainder
// (group 3).
func rewriteRootRel(content, root string, re *regexp.Regexp) string {
	locs := re.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return content
	}
	var b strings.Builder
	b.Grow(len(content) + 64*len(locs))
	last := 0
	for _, loc := range locs {
		if loc[5] < len(content) && content[loc[5]] == '/' { // "//…" → protocol-relative
			continue
		}
		b.WriteString(content[last:loc[0]])
		b.WriteString(content[loc[2]:loc[3]]) // prefix incl. opening quote
		b.WriteString(root)
		b.WriteString(content[loc[6]:loc[7]])
		last = loc[1]
	}
	b.WriteString(content[last:])
	return b.String()
}

func (ps *previewStore) get(token string) *previewDoc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	doc, ok := ps.docs[token]
	if !ok {
		return nil
	}
	// A reader cannot bring an expired stub back, so renewing it would only keep a dead token around.
	if !doc.expired {
		doc.lastAccess = time.Now()
	}
	return doc
}

func (ps *previewStore) delete(token string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.docs, token)
}

// recordNav notes a committed page load. Only the success paths reach it, so a record always says
// the response committed.
func (ps *previewStore) recordNav(token, rel string, isDoc bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if doc, exists := ps.docs[token]; exists {
		doc.navSeq++
		doc.lastNav = previewNavRecord{path: rel, isDoc: isDoc}
	}
}

func (ps *previewStore) lastNav(token string) (seq int, rec previewNavRecord, ok bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	doc, exists := ps.docs[token]
	if !exists {
		return 0, previewNavRecord{}, false
	}
	return doc.navSeq, doc.lastNav, true
}

func (ps *previewStore) sweep(now time.Time) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for tok, d := range ps.docs {
		if d.expired || now.Sub(d.lastAccess) > previewTTL {
			delete(ps.docs, tok)
			continue
		}
		if now.Sub(d.createdAt) > previewMaxAge {
			// The hard limit: the content goes now and the token stays as a stub, so the route can
			// answer "preview expired" where an unknown token answers "preview not found".
			d.content, d.baseDir, d.expired = "", "", true
		}
	}
}

func (ps *previewStore) evictOldestLocked() {
	var oldest string
	var oldestTime time.Time
	for tok, d := range ps.docs {
		if oldest == "" || d.lastAccess.Before(oldestTime) {
			oldest, oldestTime = tok, d.lastAccess
		}
	}
	delete(ps.docs, oldest)
}

// previewRand is the entropy source behind preview tokens. Package-level seam so
// tests can exercise the mint-failure path; production always reads crypto/rand.
var previewRand io.Reader = rand.Reader

// newPreviewToken — 128 bits of randomness; the only credential for the public
// document/asset GETs. Minting is fallible on purpose: a guessable token is an
// unauthenticated read of the document and every sibling under its asset root, so
// a failed entropy read fails the request instead of falling back.
func newPreviewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(previewRand, b); err != nil {
		return "", fmt.Errorf("mint preview token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// previewServeRequest: only `content` and `posture` come from the client; the host fills
// project/path/task so the server can resolve sibling assets.
type previewServeRequest struct {
	Content string `json:"content"`
	Project string `json:"project"`
	Path    string `json:"path"`
	Task    string `json:"task"`
	Posture string `json:"posture"`
}

// HandlePreviewServe — POST /api/preview (session + CSRF). url is the doc's
// directory under the token, so relative refs resolve against the file's
// siblings.
func (s *Server) HandlePreviewServe(w http.ResponseWriter, r *http.Request) {
	// +8 KB slack over the cap for JSON framing/escaping.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, previewMaxContent+8192))
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			JSONError(w, http.StatusRequestEntityTooLarge, "preview content too large")
			return
		}
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var req previewServeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Content) > previewMaxContent {
		JSONError(w, http.StatusRequestEntityTooLarge, "preview content too large")
		return
	}

	baseDir, dirRel, fileName := "", "", ""
	if req.Project != "" || req.Path != "" {
		worktreeRoot, dir, hostPath, err := s.resolveFSFilePath(req.Project, req.Task, req.Path)
		if err != nil {
			// A project or task that has gone is a 404, and a store fault is a 500 whose cause stays in
			// the log. What is left is this request's own path being refused.
			if !errs.IsNotFound(err) && !errs.IsOperation(err) {
				JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			Fail(w, err, "resolve preview file", "project not found")
			return
		}
		fileName = filepath.Base(hostPath)
		baseDir = worktreeRoot // widened asset root: the whole worktree
		rel, err := filepath.Rel(worktreeRoot, dir)
		if err != nil || !fsutil.RelInsideRoot(rel) {
			// The file is outside the worktree (a workspace-only path): use
			// its own directory as the asset root instead.
			baseDir = dir
		} else if rel != "." {
			dirRel = filepath.ToSlash(rel)
		}
	}

	token, err := s.previewStore.put(req.Content, fileName, baseDir, dirRel, previewPostureFrom(req.Posture))
	if err != nil {
		if errors.Is(err, errPreviewTooLarge) {
			JSONError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		// Token mint failure: never emit a weak token.
		logging.For(logging.HTTP).Errorf("preview serve failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "preview unavailable")
		return
	}
	url := "/api/preview/" + token + "/"
	if dirRel != "" {
		url += dirRel + "/"
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"token": token,
		"url":   url,
	})
}

// HandlePreviewGet — public GET /api/preview/{token}[/...]; the token is
// the only credential (the preview iframe has an opaque origin and can't
// send a session cookie). The doc serves at its own directory under the
// token; other paths are sibling files from the worktree. Sibling pages
// carry the doc's CSP, so a page the doc navigates to keeps the same egress
// posture.
func (s *Server) HandlePreviewGet(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	doc := s.previewStore.get(token)
	// A real page load (not a css/js/img subresource)? The preview frame is a
	// subframe, and Chromium sends Sec-Fetch-Dest: "iframe" for in-subframe
	// navigations, not "document".
	isDocNav := func() bool {
		dest := r.Header.Get("Sec-Fetch-Dest")
		return dest == "document" || dest == "iframe"
	}
	if doc == nil {
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}
	if doc.expired {
		// The content is gone either way; the wording tells a caller whether re-serving would help.
		JSONError(w, http.StatusNotFound, "preview expired")
		return
	}
	rel := strings.TrimPrefix(r.PathValue("path"), "/")
	rel = strings.TrimSuffix(rel, "/") // directory URLs may carry a trailing slash

	writeDoc := func() {
		w.Header().Set("Content-Type", previewDocContentType(doc.fileName))
		w.Header().Set("Content-Security-Policy", previewDocCSP(doc))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, doc.content)
		}
		if isDocNav() {
			s.previewStore.recordNav(token, rel, true)
		}
	}

	// The token root: the doc's directory when the file is at the worktree
	// root, otherwise the worktree root itself (no listing).
	if rel == "" {
		if doc.dirRel == "" {
			if !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, "/api/preview/"+token+"/", http.StatusMovedPermanently)
				return
			}
			writeDoc()
			return
		}
		if r.URL.Path == "/api/preview/"+token {
			http.Redirect(w, r, "/api/preview/"+token+"/"+doc.dirRel+"/", http.StatusMovedPermanently)
			return
		}
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}

	// The doc's own directory: /token/<dirRel>/ (a file at the worktree root
	// has dirRel "" and was handled at the token root above).
	if rel == doc.dirRel {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, "/api/preview/"+token+"/"+doc.dirRel+"/", http.StatusMovedPermanently)
			return
		}
		writeDoc()
		return
	}

	// Self-reference: a link to the doc's own file name serves the stored
	// content (possibly unsaved), not the file on disk.
	if doc.fileName != "" {
		selfRel := doc.fileName
		if doc.dirRel != "" {
			selfRel = doc.dirRel + "/" + doc.fileName
		}
		if strings.EqualFold(rel, selfRel) {
			writeDoc()
			return
		}
	}

	if doc.baseDir == "" {
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}
	// A checkout's `.git` is never one of a page's assets: its config can carry a credential. The type
	// rule below already refuses what a real checkout holds, and neither rule opens what it refuses.
	if fsutil.HasGitSegment(rel) {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	ct, ctOK := previewAssetContentType(rel)
	if !ctOK {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	file, err := s.resolvePreviewAsset(doc.baseDir, rel)
	if err != nil {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	// The doc's directory is the task's own: lstat the name, then open the file that lstat saw.
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	if before.Size() > previewMaxAsset {
		JSONError(w, http.StatusRequestEntityTooLarge, "preview asset too large")
		return
	}
	f, info, err := fsutil.OpenVerified(file, before)
	if err != nil {
		if errors.Is(err, fsutil.ErrSwapped) {
			logging.For(logging.HTTP).Warnf("preview asset refused, path changed after containment: %s", rel)
		}
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	// docs/preview.md → A previewed page can embed, it cannot read.
	logging.For(logging.HTTP).Debugf("preview sibling served: %s (loaded, not readable by the page's own scripts)", rel)
	if isHTMLFamilyCT(ct) {
		// A sibling page the doc navigates to must keep the doc's CSP;
		// otherwise the posture could be skipped by linking to a sibling .html.
		w.Header().Set("Content-Security-Policy", previewDocCSP(doc))
		if isDocNav() {
			s.previewStore.recordNav(token, rel, false)
		}
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, f, info.Size())
	}
}

// HandlePreviewNav — GET /api/preview-nav/{token} (session-authed). Returns
// the last page load under the token; the extension asks this after a frame
// load it didn't start, and allows the navigation only if a fresh record
// came back.
func (s *Server) HandlePreviewNav(w http.ResponseWriter, r *http.Request) {
	seq, nav, ok := s.previewStore.lastNav(r.PathValue("token"))
	if !ok {
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"seq":  seq,
		"path": nav.path,
		// Derived, not stored: a record exists exactly when a page committed. Installed extensions read this key.
		"ok":    seq > 0,
		"isDoc": nav.isDoc,
	})
}

// HandlePreviewDelete — DELETE /api/preview/{token} (session + CSRF); the
// TTL sweep is the backstop.
func (s *Server) HandlePreviewDelete(w http.ResponseWriter, r *http.Request) {
	s.previewStore.delete(r.PathValue("token"))
	JSONResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// resolvePreviewAsset resolves rel under baseDir, rejecting traversal and
// symlink escapes.
func (s *Server) resolvePreviewAsset(baseDir, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", errors.New("invalid path")
	}
	// Collapse ".." segments before joining, so every path left to judge is one the
	// containment check can take at face value.
	candidate := filepath.Join(baseDir, filepath.Clean("/"+filepath.ToSlash(rel)))
	// The jail every host file served to a browser goes through: the lexical path and the
	// symlink-resolved path must both sit inside the root.
	return fsutil.Contained(candidate, []string{baseDir})
}

// previewAssetCT is what one preview token will hand over: the types a browser embeds as a subresource,
// plus the HTML family an in-tree navigation lands on. There is no fallback type for an unmapped name,
// because one would turn a leaked token into an unauthenticated read of everything under its root
// (`.env`, `.git/config`); a type is listed twice where platforms disagree about which name they answer.
var previewAssetCT = map[string]bool{
	// HTML family. A sibling page reached by an in-tree link is served here, and carries the doc's CSP.
	"text/html": true, "application/xhtml+xml": true, "image/svg+xml": true,
	// A page's own scripts and styles. Loading is not executing (that is script-src), and not reading.
	"text/javascript": true, "application/javascript": true,
	"text/css":  true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"image/bmp": true, "image/apng": true, "image/tiff": true, "image/x-icon": true,
	"image/vnd.microsoft.icon": true,
	"font/woff":                true, "font/woff2": true, "font/ttf": true, "font/otf": true, "font/collection": true,
	"application/font-woff": true, "application/font-woff2": true, "application/vnd.ms-fontobject": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true, "video/quicktime": true,
	"audio/mpeg": true, "audio/ogg": true, "audio/wav": true, "audio/x-wav": true, "audio/mp4": true,
	"audio/aac": true, "audio/flac": true, "audio/webm": true,
}

// previewAssetContentType: the extension's type, and whether that type may be served at all.
func previewAssetContentType(path string) (string, bool) {
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if ct == "" {
		return "", false
	}
	// The platform database carries a parameter on the text types.
	base := ct
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	return ct, previewAssetCT[base]
}

// resolveWorktreeForFile: host worktree for project/task (task worktree or
// project root) + /home/sandbox/<name>/… → host path translation. The task is
// honoured only when it belongs to the project — see requireTaskOwnership.
func (s *Server) resolveWorktreeForFile(project *types.Project, taskID, path string) (string, string, error) {
	if err := s.requireTaskOwnership(project, taskID); err != nil {
		return "", "", err
	}
	var worktreePath string
	if taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			return "", "", err
		}
		worktreePath = wtPath
	} else {
		worktreePath = project.Path
	}
	if project.SandboxEnabled {
		if host, ok := sandbox.ContainerToHost(worktreePath, project.Name, path); ok {
			path = host
		}
	}
	return worktreePath, path, nil
}

// resolveFSFilePath resolves an editor file path to (worktreeRoot, dir, hostPath). worktreeRoot is
// the preview's asset root, so the file must be inside the project's checkout or the worktree of
// the task the request names — never the workspace dir, which holds every project's task worktrees.
func (s *Server) resolveFSFilePath(projectID, taskID, filePath string) (worktreeRoot, dir, hostPath string, err error) {
	if projectID == "" || filePath == "" {
		return "", "", "", errors.New("project and path are required")
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		// Propagated as a classified not-found (message unchanged) rather than a fresh
		// errors.New, so a caller can still tell "absent" from "database fault".
		if errs.IsNotFound(err) {
			return "", "", "", fmt.Errorf("project %w", errs.ErrNotFound)
		}
		return "", "", "", errs.Operation("read project", err)
	}
	worktreePath, hostPath, err := s.resolveWorktreeForFile(project, taskID, filePath)
	if err != nil {
		if errs.IsNotFound(err) {
			return "", "", "", fmt.Errorf("task worktree %w", errs.ErrNotFound)
		}
		return "", "", "", errs.Operation("resolve worktree", err)
	}
	// Relative paths resolve against the worktree root defensively.
	if !filepath.IsAbs(hostPath) {
		hostPath = filepath.Join(worktreePath, hostPath)
	}

	cleanPath := filepath.Clean(hostPath)
	cleanRoot := filepath.Clean(project.Path)
	cleanWorktree := filepath.Clean(worktreePath)

	// The preview serves a document's siblings, so this root is what a token exposes:
	// the project checkout plus the worktree of the task the request named, never the
	// workspace dir (it holds every project's task worktrees).
	roots := []string{cleanRoot}
	if cleanWorktree != cleanRoot {
		roots = append(roots, cleanWorktree)
	}
	if _, err := fsutil.Contained(cleanPath, roots); err != nil {
		return "", "", "", err
	}
	return cleanWorktree, filepath.Dir(cleanPath), cleanPath, nil
}
