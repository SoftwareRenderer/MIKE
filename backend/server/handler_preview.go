package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

// previewDocCSP is deliberately permissive so a page renders like it would
// in a regular browser. Host protection comes from the sandbox model (nested
// sandbox="allow-scripts" iframe → opaque origin: no cookies, no host DOM, no
// top navigation; mutations also need CSRF), not from this policy.
// frame-ancestors must be ABSENT — the doc must stay frameable; the 128-bit
// token is the access credential. Full model: AGENTS.md "Preview capability".
const previewDocCSP = "default-src * data: blob: 'unsafe-inline'; script-src * 'unsafe-inline' data: blob:; style-src * 'unsafe-inline' data: blob:; img-src * data: blob:; font-src * data: blob:; connect-src * data: blob:; frame-src * data: blob:; worker-src * blob: data:; object-src 'none'; base-uri 'none'; form-action 'none'"

// previewDoc is a served preview document.
type previewDoc struct {
	content    string // document text (HTML/SVG/XHTML/…)
	fileName   string // previewed file's base name ("" if unknown)
	baseDir    string // host dir for relative assets ("" = self-contained)
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

func (ps *previewStore) put(content, fileName, baseDir string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for len(ps.docs) >= previewMaxLive {
		ps.evictOldestLocked()
	}
	token := newPreviewToken()
	ps.docs[token] = &previewDoc{content: content, fileName: fileName, baseDir: baseDir, lastAccess: time.Now()}
	return token
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

// newPreviewToken returns a 128-bit random token (32 hex chars) — the sole
// bearer for the public document/asset GETs.
func newPreviewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is a fatal environment problem; don't fail the request.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// previewServeRequest: only `content` comes from the client; project/path/task
// identify the editor's open file (host-filled) for sibling-asset resolution.
type previewServeRequest struct {
	Content string `json:"content"`
	Project string `json:"project"`
	Path    string `json:"path"`
	Task    string `json:"task"`
}

// HandlePreviewServe — POST /api/preview (session + CSRF). Stores the doc and
// returns {token, url}; url ends in "/" so relative asset URLs resolve under it.
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

	baseDir, fileName := "", ""
	if req.Project != "" || req.Path != "" {
		dir, hostPath, err := s.resolveFSFilePath(req.Project, req.Task, req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		baseDir = dir
		fileName = filepath.Base(hostPath)
	}

	token := s.previewStore.put(req.Content, fileName, baseDir)
	JSONResponse(w, http.StatusOK, map[string]any{
		"token": token,
		"url":   "/api/preview/" + token + "/",
	})
}

// HandlePreviewGet — public GET /api/preview/{token}[/...]; the token is the
// bearer (opaque-origin preview iframes can't send a session). Empty subpath
// → the doc (no-slash → trailing-slash redirect); otherwise a sibling asset.
func (s *Server) HandlePreviewGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	doc := s.previewStore.get(token)
	if doc == nil {
		JSONError(w, http.StatusNotFound, "preview not found")
		return
	}
	rel := strings.TrimPrefix(r.PathValue("path"), "/")

	writeDoc := func() {
		w.Header().Set("Content-Type", previewDocContentType(doc.fileName))
		w.Header().Set("Content-Security-Policy", previewDocCSP)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, doc.content)
		}
	}

	if rel == "" {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, "/api/preview/"+token+"/", http.StatusMovedPermanently)
			return
		}
		writeDoc()
		return
	}

	// Self-reference (<a href="index.html">) → served content (possibly
	// unsaved), not the disk file.
	if doc.fileName != "" && strings.EqualFold(rel, doc.fileName) {
		writeDoc()
		return
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
	w.Header().Set("Content-Type", previewAssetContentType(file))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, f, info.Size())
	}
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

// resolveFSFilePath resolves an editor file path to (dir, hostPath),
// confined to the project root or the configured workspace directory.
func (s *Server) resolveFSFilePath(projectID, taskID, filePath string) (dir, hostPath string, err error) {
	if projectID == "" || filePath == "" {
		return "", "", errors.New("project and path are required")
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return "", "", errors.New("project not found")
	}
	worktreePath, hostPath, err := s.resolveWorktreeForFile(project, taskID, filePath)
	if err != nil {
		return "", "", errors.New("task worktree not found")
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
	inProject := cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator))
	inWorkspace := workspaceDir != "" && (cleanPath == workspaceDir || strings.HasPrefix(cleanPath, workspaceDir+string(os.PathSeparator)))
	if !inProject && !inWorkspace {
		return "", "", errors.New("path outside allowed project directory")
	}
	return filepath.Dir(cleanPath), cleanPath, nil
}
