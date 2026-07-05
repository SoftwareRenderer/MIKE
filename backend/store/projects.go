package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"kanban/backend/types"
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
	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, path, origin_branch, sandbox_enabled, sandbox_network_isolation, sandbox_image, sandbox_dockerfile_path, sandbox_memory_limit, sandbox_forward_ports, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, ptrStr(p.OriginBranch), b2i(p.SandboxEnabled), b2i(p.SandboxNetworkIsolation), ptrStr(p.SandboxImage), ptrStr(p.SandboxDockerfilePath), memoryLimit, forwardPortsJSON, p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetProject retrieves a project by ID.
func (s *Store) GetProject(id string) (*types.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryProject(id)
}

func (s *Store) queryProject(id string) (*types.Project, error) {
	var p types.Project
	var originBranch, sandboxImage, sandboxDockerfilePath, forwardPortsJSON sql.NullString
	var sandboxEnabled, sandboxNetworkIsolation int
	var createdAt, updatedAt string
	err := s.db.QueryRow(`SELECT id, name, path, origin_branch, sandbox_enabled, sandbox_network_isolation, sandbox_image, sandbox_dockerfile_path, sandbox_memory_limit, sandbox_forward_ports, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Path, &originBranch, &sandboxEnabled, &sandboxNetworkIsolation, &sandboxImage, &sandboxDockerfilePath, &p.SandboxMemoryLimit, &forwardPortsJSON, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	p.OriginBranch = originBranch.String
	p.SandboxEnabled = i2b(sandboxEnabled)
	p.SandboxNetworkIsolation = i2b(sandboxNetworkIsolation)
	p.SandboxImage = sandboxImage.String
	p.SandboxDockerfilePath = sandboxDockerfilePath.String
	if forwardPortsJSON.Valid && forwardPortsJSON.String != "" {
		_ = json.Unmarshal([]byte(forwardPortsJSON.String), &p.ForwardPorts)
	}
	p.CreatedAt = scanTime(createdAt)
	p.UpdatedAt = scanTime(updatedAt)
	return &p, nil
}

// ListProjects returns all projects ordered by creation time (oldest first).
func (s *Store) ListProjects() ([]types.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, name, path, origin_branch, sandbox_enabled, sandbox_network_isolation, sandbox_image, sandbox_dockerfile_path, sandbox_memory_limit, sandbox_forward_ports, created_at, updated_at FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []types.Project{}
	for rows.Next() {
		var p types.Project
		var originBranch, sandboxImage, sandboxDockerfilePath, forwardPortsJSON sql.NullString
		var sandboxEnabled, sandboxNetworkIsolation int
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &originBranch, &sandboxEnabled, &sandboxNetworkIsolation, &sandboxImage, &sandboxDockerfilePath, &p.SandboxMemoryLimit, &forwardPortsJSON, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.OriginBranch = originBranch.String
		p.SandboxEnabled = i2b(sandboxEnabled)
		p.SandboxNetworkIsolation = i2b(sandboxNetworkIsolation)
		p.SandboxImage = sandboxImage.String
		p.SandboxDockerfilePath = sandboxDockerfilePath.String
		if forwardPortsJSON.Valid && forwardPortsJSON.String != "" {
			_ = json.Unmarshal([]byte(forwardPortsJSON.String), &p.ForwardPorts)
		}
		p.CreatedAt = scanTime(createdAt)
		p.UpdatedAt = scanTime(updatedAt)
		projects = append(projects, p)
	}
	return projects, rows.Err()
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

// UpdateProjectOriginBranch updates the origin branch for a project.
func (s *Store) UpdateProjectOriginBranch(id string, branch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET origin_branch = ?, updated_at = ? WHERE id = ?`, branch, now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxEnabled updates the sandbox_enabled flag for a project.
func (s *Store) UpdateProjectSandboxEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET sandbox_enabled = ?, updated_at = ? WHERE id = ?`, b2i(enabled), now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxNetworkIsolation updates the sandbox_network_isolation flag for a project.
func (s *Store) UpdateProjectSandboxNetworkIsolation(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET sandbox_network_isolation = ?, updated_at = ? WHERE id = ?`, b2i(enabled), now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxImage updates the sandbox_image for a project.
func (s *Store) UpdateProjectSandboxImage(id string, image string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET sandbox_image = ?, updated_at = ? WHERE id = ?`, ptrStr(image), now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxDockerfilePath updates the sandbox_dockerfile_path for a project.
func (s *Store) UpdateProjectSandboxDockerfilePath(id string, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET sandbox_dockerfile_path = ?, updated_at = ? WHERE id = ?`, ptrStr(path), now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxMemoryLimit updates the sandbox_memory_limit for a project.
func (s *Store) UpdateProjectSandboxMemoryLimit(id string, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET sandbox_memory_limit = ?, updated_at = ? WHERE id = ?`, limit, now.Format(time.RFC3339), id)
	return err
}

// UpdateProjectSandboxForwardPorts updates the sandbox_forward_ports for a project.
func (s *Store) UpdateProjectSandboxForwardPorts(id string, ports []types.PortForward) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	portsJSON := ""
	if len(ports) > 0 {
		if b, err := json.Marshal(ports); err == nil {
			portsJSON = string(b)
		}
	}
	_, err := s.db.Exec(`UPDATE projects SET sandbox_forward_ports = ?, updated_at = ? WHERE id = ?`, portsJSON, now.Format(time.RFC3339), id)
	return err
}

// DeleteProject removes a project row (relies on CASCADE for related data).
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// DeleteProjectWithData explicitly deletes all data related to a project,
// including tasks, messages, threads, dependencies, reviews, home agent messages,
// and token stats. This is the primary cleanup mechanism — CASCADE constraints
// serve as a safety net.
func (s *Store) DeleteProjectWithData(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete task-level data first (FK to tasks)
	_, _ = s.db.Exec(`DELETE FROM task_messages WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id)
	_, _ = s.db.Exec(`DELETE FROM task_threads WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id)
	_, _ = s.db.Exec(`DELETE FROM task_dependencies WHERE parent_task_id IN (SELECT id FROM tasks WHERE project_id = ?) OR child_task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id, id)
	_, _ = s.db.Exec(`DELETE FROM review_comments WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id)

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
		var projectID, status string
		var count int
		if err := rows.Scan(&projectID, &status, &count); err != nil {
			continue
		}
		c := counts[projectID]
		switch status {
		case "backlog":
			c.Backlog = count
		case "in_progress":
			c.InProgress = count
		case "review":
			c.Review = count
		}
		counts[projectID] = c
	}
	return counts
}
