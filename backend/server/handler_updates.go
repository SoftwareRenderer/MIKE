package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/types"
	"mike/backend/updates"
	"mike/pkg/logging"
)

// updateSettings is the browser's half of the "who decided" answer: the stored preference, and whether
// anybody ever gave one. The organization switch is not here — the service owns it, so a caller cannot
// forget it.
func (s *Server) updateSettings() updates.Settings {
	var pref *bool
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		// A config read that fails is not "off forever"; GetConfig logs its own cause.
		logging.For(logging.Updates).Warnf("update preference unavailable: %v", err)
	} else {
		pref = cfg.UpdateCheckEnabled
	}
	return updates.Settings{Enabled: pref != nil && *pref, Answered: pref != nil}
}

// The update routes. Five of them, and the pair that changes anything is deliberately split: fetching and
// verifying a release is one click, applying it is another. They are session + CSRF routes and stay out of
// tokenScope: a bearer token is what a headless client carries, and no headless client needs to replace
// this server's binary. docs/auto-update.md.

// handleUpdateStatus answers what is already known — the last check's result, whether checks are on, what
// this install could do — and performs no egress, so the Settings dot costs a local read.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httputil.JSONResponse(w, http.StatusOK, s.updatesService.Status(s.updateSettings()))
}

// handleUpdateCheck fetches the manifest now rather than answering from the cache. It answers with the
// status either way: a failed check leaves the last good answer true, and the failure rides in the body as
// `last_error`.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	status, err := s.updatesService.Check(r.Context(), s.updateSettings())
	if err != nil {
		// Checks being off is the caller's own doing, and the answer says which switch said so.
		httputil.Fail(w, err, "check for updates", "no update result is known")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, status)
}

// handleUpdatePrepare downloads and verifies the newest release. This is the automatic half: the client
// runs it after a check, and nothing the server runs changes as a result of it.
func (s *Server) handleUpdatePrepare(w http.ResponseWriter, r *http.Request) {
	// Detached: a tab closing halfway through a 20 MB download must not decide whether this install gets
	// the release, and a shutdown must still be able to stop the copy phase (sideeffects.go).
	ctx, cancel := s.Detached()
	defer cancel()
	info, err := s.updatesService.Prepare(ctx)
	if err != nil {
		httputil.Fail(w, err, "download the release", "no release is available to download")
		return
	}
	logging.For(logging.Updates).Infof("release %s downloaded by a request; it waits for the install click", info.Version)
	httputil.JSONResponse(w, http.StatusOK, info)
}

// handleUpdateApply is the click that changes anything: the release is swapped into place and the process
// then ends the way a signal would, so the new files are what starts next.
//
// It refuses while a task turn is running. The swap replaces the binary every worktree's commits, merges
// and container starts are being driven by, and an update that arrives in the middle of somebody's turn
// is an update that arrives in the middle of their turn.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if running, err := s.store.GetInFlightTasks(); err != nil {
		httputil.Fault(w, "check for running tasks", err)
		return
	} else if len(running) > 0 {
		httputil.JSONError(w, http.StatusConflict, describeRunning(running))
		return
	}

	// Detached, and cancelled here rather than by the base context: a swap outlives the tab that asked
	// for it, and a shutdown must be able to stop the copy pass before the renames (sideeffects.go).
	ctx, cancel := s.Detached()
	defer cancel()
	result, err := s.updatesService.Apply(ctx)
	if err != nil {
		httputil.Fail(w, err, "install the release", "no release is downloaded to install")
		return
	}
	logging.For(logging.Updates).Infof("release %s installed by a request", result.Version)

	// The click that installs is the click that restarts. Where this process has no restart path the
	// files are already in place, so that is answered as a success that says how to finish it by hand.
	answer := applyAnswer{ApplyResult: result, Restarting: true}
	if err := s.updatesService.Restart(); err != nil {
		answer.Restarting = false
		answer.RestartNote = err.Error()
	}
	httputil.JSONResponse(w, http.StatusOK, answer)
}

// applyAnswer is the install result plus how the restart went, which is the server's business rather than
// the release's.
type applyAnswer struct {
	updates.ApplyResult
	Restarting  bool   `json:"restarting"`
	RestartNote string `json:"restart_note,omitempty"`
}

// handleUpdateRestart applies an installed release by ending this process the way a signal would, so the
// shutdown is the declared stage list rather than a second one invented here. It only wakes main's select,
// so this response is written before anything stops.
func (s *Server) handleUpdateRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.updatesService.Restart(); err != nil {
		httputil.Fail(w, err, "restart to apply the update", "no release is staged")
		return
	}
	status := s.updatesService.Status(s.updateSettings())
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"restarting": true,
		"applies":    status.State.StagedVersion,
	})
}

// describeRunning is the refusal a person acts on: how much work is in the way, and which tasks.
func describeRunning(running []types.Task) string {
	titles := make([]string, 0, 3)
	for _, t := range running {
		if len(titles) == 3 {
			break
		}
		titles = append(titles, t.Title)
	}
	rest := ""
	if extra := len(running) - len(titles); extra > 0 {
		rest = " and " + strconv.Itoa(extra) + " more"
	}
	return "these tasks are running: " + strings.Join(titles, ", ") + rest +
		" — an update is not applied while work is in flight"
}

// updateStateStore is the one row the update service caches its answer in. The store answers a missing key
// with sql.ErrNoRows, which for a cache means "nothing stored yet", not a warning worth logging.
type updateStateStore struct{ store *store.Store }

func (u updateStateStore) GetConfigValue(key string) (string, error) {
	value, err := u.store.GetConfigValue(key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (u updateStateStore) SetConfigValue(key, value string) error {
	return u.store.SetConfigValue(key, value)
}
