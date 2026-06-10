package store

import (
	"database/sql"
	"fmt"
	"time"

	"kanban/backend/types"
)

// CreateProject inserts a new project.
func (s *Store) CreateProject(p *types.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
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
	var createdAt, updatedAt string
	err := s.db.QueryRow(`SELECT id, name, path, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Path, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	p.CreatedAt = scanTime(createdAt)
	p.UpdatedAt = scanTime(updatedAt)
	return &p, nil
}

// ListProjects returns all projects ordered by name.
func (s *Store) ListProjects() ([]types.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, name, path, created_at, updated_at FROM projects ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []types.Project{}
	for rows.Next() {
		var p types.Project
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &createdAt, &updatedAt); err != nil {
			return nil, err
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

// DeleteProject removes a project.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
