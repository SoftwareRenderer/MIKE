package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/types"
)

// Preview document lifecycle limits.
const (
	previewTTL         = 10 * time.Minute // idle before GC; every read refreshes
	previewMaxLive     = 32               // live doc cap; oldest evicted
	previewMaxContent  = 10 << 20         // doc size cap
	previewMaxAsset    = 25 << 20         // sibling asset size cap
	previewSweepPeriod = 30 * time.Second // TTL sweep interval
)

// previewDocCSPStrict is the DEFAULT policy
const previewDocCSPStrict = "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' data: blob:; style-src 'self' 'unsafe-inline' data: blob:; img-src 'self' data: blob:; font-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'self' blob: data:; object-src 'none'; base-uri 'none'; form-action 'none'"

// previewDocCSPNetwork — the permissive policy; served only when the
// extension's "allow network" setting opts in per serve
const previewDocCSPNetwork = "default-src * data: blob: 'unsafe-inline'; script-src * 'unsafe-inline' data: blob:; style-src * 'unsafe-inline' data: blob:; img-src * data: blob:; font-src * data: blob:; connect-src * data: blob:; frame-src * data: blob:; worker-src * blob: data:; object-src 'none'; base-uri 'none'; form-action 'none'"

// previewNavRecord — only committed HTML-family page loads are recorded
// (see isDocNav). A 404 or an out-of-tree navigation leaves the record
// stale, and the extension's watchdog rejects any navigation that didn't
// produce a fresh record.
type previewNavRecord struct {
	path  string // token-relative path ("" = the doc's own dir)
	ok    bool   // the response committed (2xx)
	isDoc bool   // the doc's own content (dir URL or self-reference)
}

// previewDoc is a served preview document.
type previewDoc struct {
	content      string
	fileName     string // previewed file's base name ("" if unknown)
	baseDir      string // worktree root, or the file's own dir when outside it ("" = self-contained)
	dirRel       string // doc's dir relative to baseDir ("" = baseDir itself)
	allowNetwork bool   // the extension's "allow network" opt-in (true: permissive CSP, false: strict)
	navSeq       int    // committed page loads under this token; the watchdog compares it to detect a fresh load
	lastNav      previewNavRecord
	lastAccess   time.Time
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

func (ps *previewStore) put(content, fileName, baseDir, dirRel string, allowNetwork bool) (string, error) {
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
	ps.docs[token] = &previewDoc{content: content, fileName: fileName, baseDir: baseDir, dirRel: dirRel, allowNetwork: allowNetwork, lastAccess: time.Now()}
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
	if doc.allowNetwork {
		return previewDocCSPNetwork
	}
	return previewDocCSPStrict
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
	doc.lastAccess = time.Now()
	return doc
}

func (ps *previewStore) delete(token string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.docs, token)
}

func (ps *previewStore) recordNav(token, rel string, ok, isDoc bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if doc, exists := ps.docs[token]; exists {
		doc.navSeq++
		doc.lastNav = previewNavRecord{path: rel, ok: ok, isDoc: isDoc}
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
		if now.Sub(d.lastAccess) > previewTTL {
			delete(ps.docs, tok)
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

// previewServeRequest: only `content` comes from the client. The host fills
// project/path/task (the editor's open file) so the server can resolve
// sibling assets. AllowNetwork is the extension's opt-in; it only widens the
// doc's CSP (default: strict).
type previewServeRequest struct {
	Content      string `json:"content"`
	Project      string `json:"project"`
	Path         string `json:"path"`
	Task         string `json:"task"`
	AllowNetwork bool   `json:"allow_network"`
}

// HandlePreviewServe — POST /api/preview (session + CSRF). url is the doc's
// directory under the token, so relative refs resolve against the file's
// siblings.
func (s *Server) HandlePreviewServe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		fileName = filepath.Base(hostPath)
		baseDir = worktreeRoot // widened asset root: the whole worktree
		rel, err := filepath.Rel(worktreeRoot, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// The file is outside the worktree (a workspace-only path): use
			// its own directory as the asset root instead.
			baseDir = dir
		} else if rel != "." {
			dirRel = filepath.ToSlash(rel)
		}
	}

	token, err := s.previewStore.put(req.Content, fileName, baseDir, dirRel, req.AllowNetwork)
	if err != nil {
		if errors.Is(err, errPreviewTooLarge) {
			JSONError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		// Token mint failure: never emit a weak token.
		log.Printf("preview serve failed: %v", err)
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
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
			s.previewStore.recordNav(token, rel, true, true)
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
	file, err := s.resolvePreviewAsset(doc.baseDir, rel)
	if err != nil {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	if info.Size() > previewMaxAsset {
		JSONError(w, http.StatusRequestEntityTooLarge, "preview asset too large")
		return
	}
	f, err := os.Open(file)
	if err != nil {
		JSONError(w, http.StatusNotFound, "preview asset not found")
		return
	}
	defer f.Close()
	ct := previewAssetContentType(file)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	if isHTMLFamilyCT(ct) {
		// A sibling page the doc navigates to must keep the doc's CSP;
		// otherwise the posture could be skipped by linking to a sibling .html.
		w.Header().Set("Content-Security-Policy", previewDocCSP(doc))
		if isDocNav() {
			s.previewStore.recordNav(token, rel, true, false)
		}
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, f, info.Size())
	}
}

// HandlePreviewNav — GET /api/preview/{token}/nav (session-authed). Returns
// the last page load under the token; the extension asks this after a frame
// load it didn't start, and allows the navigation only if a fresh record
// came back. A file at the worktree root literally named "nav" is hidden by
// this route (documented limitation).
func (s *Server) HandlePreviewNav(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	seq, nav, ok := s.previewStore.lastNav(r.PathValue("token"))
	if !ok {
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"seq":   seq,
		"path":  nav.path,
		"ok":    nav.ok,
		"isDoc": nav.isDoc,
	})
}

// HandlePreviewDelete — DELETE /api/preview/{token} (session + CSRF); the
// TTL sweep is the backstop.
func (s *Server) HandlePreviewDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.previewStore.delete(r.PathValue("token"))
	JSONResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// resolvePreviewAsset resolves rel under baseDir, rejecting traversal and
// symlink escapes.
func (s *Server) resolvePreviewAsset(baseDir, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", errors.New("invalid path")
	}
	// Collapse ".." segments.
	cleaned := filepath.Clean("/" + filepath.ToSlash(rel))
	candidate := filepath.Join(baseDir, cleaned)

	base, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", errors.New("path escapes preview directory")
	}
	return target, nil
}

// previewAssetContentType: extension-based type, defaulting to octet-stream.
func previewAssetContentType(path string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// resolveWorktreeForFile: host worktree for project/task (task worktree or
// project root) + /home/sandbox/<name>/… → host path translation.
func (s *Server) resolveWorktreeForFile(project *types.Project, taskID, path string) (string, string, error) {
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
		mountPath := "/home/sandbox/" + project.Name
		cleanPath := filepath.Clean(path)
		if cleanPath == mountPath || strings.HasPrefix(cleanPath, mountPath+string(os.PathSeparator)) {
			rel := cleanPath[len(mountPath):]
			if rel == "" {
				path = worktreePath
			} else {
				path = filepath.Join(worktreePath, rel)
			}
		}
	}
	return worktreePath, path, nil
}

// resolveFSFilePath resolves an editor file path to (worktreeRoot, dir,
// hostPath). The path must be inside the project root or the configured
// workspace dir. worktreeRoot is the preview's asset root.
func (s *Server) resolveFSFilePath(projectID, taskID, filePath string) (worktreeRoot, dir, hostPath string, err error) {
	if projectID == "" || filePath == "" {
		return "", "", "", errors.New("project and path are required")
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return "", "", "", errors.New("project not found")
	}
	worktreePath, hostPath, err := s.resolveWorktreeForFile(project, taskID, filePath)
	if err != nil {
		return "", "", "", errors.New("task worktree not found")
	}
	// Relative paths resolve against the worktree root defensively.
	if !filepath.IsAbs(hostPath) {
		hostPath = filepath.Join(worktreePath, hostPath)
	}

	cfg, _ := s.configStore.GetConfig()
	workspaceDir := ""
	if cfg != nil && cfg.WorkspaceDir != "" {
		workspaceDir = filepath.Clean(cfg.WorkspaceDir)
	}
	cleanPath := filepath.Clean(hostPath)
	cleanRoot := filepath.Clean(project.Path)
	cleanWorktree := filepath.Clean(worktreePath)
	inProject := cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator))
	inWorkspace := workspaceDir != "" && (cleanPath == workspaceDir || strings.HasPrefix(cleanPath, workspaceDir+string(os.PathSeparator)))
	if !inProject && !inWorkspace {
		return "", "", "", errors.New("path outside allowed project directory")
	}
	return cleanWorktree, filepath.Dir(cleanPath), cleanPath, nil
}
