package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"kanban/backend/types"
	"kanban/pkg/logging"
)

// CreateProject inserts a new project.
func (s *Store) CreateProject(p *types.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	memoryLimit := p.SandboxMemoryLimit
	if memoryLimit == 0 {
		memoryLimit = 1024 // default 1 GB
	}
	forwardPortsJSON := ""
	if len(p.ForwardPorts) > 0 {
		if b, err := json.Marshal(p.ForwardPorts); err == nil {
			forwardPortsJSON = string(b)
		}
	}
	mountsJSON := ""
	if len(p.SandboxMounts) > 0 {
		if b, err := json.Marshal(p.SandboxMounts); err == nil {
			mountsJSON = string(b)
		}
	}
	envsJSON := ""
	if len(p.SandboxEnvs) > 0 {
		if b, err := json.Marshal(p.SandboxEnvs); err == nil {
			envsJSON = string(b)
		}
	}
	capsJSON := ""
	if len(p.SandboxCapabilities) > 0 {
		if b, err := json.Marshal(p.SandboxCapabilities); err == nil {
			capsJSON = string(b)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, ptrStr(p.OriginBranch), B2I(p.SandboxEnabled), B2I(p.SandboxNetworkIsolation), ptrStr(p.SandboxImage), ptrStr(p.SandboxDockerfilePath), memoryLimit, forwardPortsJSON, mountsJSON, envsJSON, capsJSON, p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// projectColumns lists the projects table columns in the exact order scanProject
// reads them. Keep the two in step — a SELECT built from anything else will scan
// silently wrong values, not fail.
const projectColumns = `id, name, path, origin_branch, sandbox_enabled, sandbox_network_isolation, sandbox_image, sandbox_dockerfile_path, sandbox_memory_limit, sandbox_forward_ports, sandbox_mounts, sandbox_envs, sandbox_capabilities, created_at, updated_at`

// scanProject reads one project row from either a *sql.Row or an *sql.Rows cursor
// and applies the column conventions (NULL-able text columns collapse to "",
// 0/1 integers to bools, JSON columns unmarshal best-effort).
func scanProject(s scanner, p *types.Project) error {
	var originBranch, sandboxImage, sandboxDockerfilePath, forwardPortsJSON, mountsJSON, envsJSON, capsJSON sql.NullString
	var sandboxEnabled, sandboxNetworkIsolation int
	var createdAt, updatedAt string
	if err := s.Scan(&p.ID, &p.Name, &p.Path, &originBranch, &sandboxEnabled, &sandboxNetworkIsolation, &sandboxImage, &sandboxDockerfilePath, &p.SandboxMemoryLimit, &forwardPortsJSON, &mountsJSON, &envsJSON, &capsJSON, &createdAt, &updatedAt); err != nil {
		return err
	}
	p.OriginBranch = originBranch.String
	p.SandboxEnabled = I2B(sandboxEnabled)
	p.SandboxNetworkIsolation = I2B(sandboxNetworkIsolation)
	p.SandboxImage = sandboxImage.String
	p.SandboxDockerfilePath = sandboxDockerfilePath.String
	if forwardPortsJSON.Valid && forwardPortsJSON.String != "" {
		_ = json.Unmarshal([]byte(forwardPortsJSON.String), &p.ForwardPorts)
	}
	if mountsJSON.Valid && mountsJSON.String != "" {
		_ = json.Unmarshal([]byte(mountsJSON.String), &p.SandboxMounts)
	}
	if envsJSON.Valid && envsJSON.String != "" {
		_ = json.Unmarshal([]byte(envsJSON.String), &p.SandboxEnvs)
	}
	if capsJSON.Valid && capsJSON.String != "" {
		_ = json.Unmarshal([]byte(capsJSON.String), &p.SandboxCapabilities)
	}
	p.CreatedAt = scanTime(createdAt)
	p.UpdatedAt = scanTime(updatedAt)
	return nil
}

// GetProject retrieves a project by ID.
func (s *Store) GetProject(id string) (*types.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryProject(id)
}

// queryProject reads one project. Callers must hold s.mu.
func (s *Store) queryProject(id string) (*types.Project, error) {
	var p types.Project
	err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id), &p)
	if err == sql.ErrNoRows {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProjects returns all projects ordered by creation time (oldest first).
func (s *Store) ListProjects() ([]types.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT ` + projectColumns + ` FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return scanRows(rows, scanInto(scanProject))
}

// UpdateProject updates a project's name.
func (s *Store) UpdateProject(id string, name string) (*types.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`, name, now.Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	p, err := s.queryProject(id)
	return p, err
}

// updateProjectColumn updates a single projects column and bumps updated_at.
// The column name comes from internal call sites only (never user input).
func (s *Store) updateProjectColumn(id, column string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec("UPDATE projects SET "+column+" = ?, updated_at = ? WHERE id = ?", value, now.Format(time.RFC3339), id)
	return err
}

// marshalColumnJSON serializes a list value for a JSON column, returning ""
// for empty or nil values (the legacy empty-column convention).
func marshalColumnJSON(v any) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map {
		if rv.IsNil() || rv.Len() == 0 {
			return ""
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// UpdateProjectOriginBranch updates the origin branch for a project.
func (s *Store) UpdateProjectOriginBranch(id string, branch string) error {
	return s.updateProjectColumn(id, "origin_branch", branch)
}

// UpdateProjectSandboxEnabled updates the sandbox_enabled flag for a project.
func (s *Store) UpdateProjectSandboxEnabled(id string, enabled bool) error {
	return s.updateProjectColumn(id, "sandbox_enabled", B2I(enabled))
}

// UpdateProjectSandboxNetworkIsolation updates the sandbox_network_isolation flag for a project.
func (s *Store) UpdateProjectSandboxNetworkIsolation(id string, enabled bool) error {
	return s.updateProjectColumn(id, "sandbox_network_isolation", B2I(enabled))
}

// UpdateProjectSandboxImage updates the sandbox_image for a project.
// An empty image resets the column to NULL (project-level image unset).
func (s *Store) UpdateProjectSandboxImage(id string, image string) error {
	return s.updateProjectColumn(id, "sandbox_image", ptrStr(image))
}

// UpdateProjectSandboxDockerfilePath updates the sandbox_dockerfile_path for a project.
// An empty path resets the column to NULL (Dockerfile build unset).
func (s *Store) UpdateProjectSandboxDockerfilePath(id string, path string) error {
	return s.updateProjectColumn(id, "sandbox_dockerfile_path", ptrStr(path))
}

// UpdateProjectSandboxMemoryLimit updates the sandbox_memory_limit for a project.
func (s *Store) UpdateProjectSandboxMemoryLimit(id string, limit int) error {
	return s.updateProjectColumn(id, "sandbox_memory_limit", limit)
}

// UpdateProjectSandboxForwardPorts updates the sandbox_forward_ports for a project.
func (s *Store) UpdateProjectSandboxForwardPorts(id string, ports []types.PortForward) error {
	return s.updateProjectColumn(id, "sandbox_forward_ports", marshalColumnJSON(ports))
}

// UpdateProjectSandboxMounts updates the sandbox_mounts for a project.
func (s *Store) UpdateProjectSandboxMounts(id string, mounts []types.SandboxMount) error {
	return s.updateProjectColumn(id, "sandbox_mounts", marshalColumnJSON(mounts))
}

// UpdateProjectSandboxEnvs updates the sandbox_envs for a project.
func (s *Store) UpdateProjectSandboxEnvs(id string, envs []types.SandboxEnv) error {
	return s.updateProjectColumn(id, "sandbox_envs", marshalColumnJSON(envs))
}

// UpdateProjectSandboxCapabilities updates the sandbox_capabilities for a project.
func (s *Store) UpdateProjectSandboxCapabilities(id string, capabilities []string) error {
	return s.updateProjectColumn(id, "sandbox_capabilities", marshalColumnJSON(capabilities))
}

// DeleteProject explicitly deletes all data related to a project,
// including tasks, messages, threads, dependencies, reviews, home agent messages,
// and token stats. This is the primary cleanup mechanism — CASCADE constraints
// serve as a safety net.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete task-level data first (FK to tasks)
	_, _ = s.db.Exec(`DELETE FROM task_messages WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id)
	_, _ = s.db.Exec(`DELETE FROM task_threads WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id)

	// Delete tasks (FK to projects)
	_, _ = s.db.Exec(`DELETE FROM tasks WHERE project_id = ?`, id)

	// Delete project-level data (FK to projects)
	_, _ = s.db.Exec(`DELETE FROM project_token_stats WHERE project_id = ?`, id)
	_, _ = s.db.Exec(`DELETE FROM project_token_tasks WHERE project_id = ?`, id)

	// Delete the project itself
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// GetAllProjectCounts returns task counts grouped by project and status.
// Only counts backlog, in_progress, and review (not done).
func (s *Store) GetAllProjectCounts() map[string]types.ProjectCounts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]types.ProjectCounts)
	rows, err := s.db.Query(
		`SELECT project_id, status, COUNT(*) FROM tasks WHERE status IN ('backlog', 'in_progress', 'review') GROUP BY project_id, status`,
	)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var projectID string
		// Typed as Status so the switch below cannot drift from the column values;
		// Scan assigns it through reflection on the underlying string kind.
		var status types.Status
		var count int
		if err := rows.Scan(&projectID, &status, &count); err != nil {
			continue
		}
		c := counts[projectID]
		switch status {
		case types.StatusBacklog:
			c.Backlog = count
		case types.StatusInProgress:
			c.InProgress = count
		case types.StatusReview:
			c.Review = count
		}
		counts[projectID] = c
	}
	// A truncated iteration would under-count a project's badge silently, and the caller
	// cannot re-check these counts.
	if err := rows.Err(); err != nil {
		logging.For(logging.Store).Infof("project task counts incomplete: %v", err)
	}
	return counts
}
