package store

import (
	"fmt"
	"time"

	"kanban/backend/types"
)

// CreateReviewComment inserts a new review comment.
func (s *Store) CreateReviewComment(c *types.ReviewComment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO review_comments (id, task_id, file_path, line_num, comment, resolved, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TaskID, c.FilePath, c.LineNum, c.Comment, b2i(c.Resolved), c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetReviewComments returns all review comments for a task.
func (s *Store) GetReviewComments(taskID string) ([]types.ReviewComment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE task_id = ? ORDER BY file_path, line_num`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []types.ReviewComment{}
	for rows.Next() {
		var c types.ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// GetUnresolvedReviewComments returns only unresolved review comments for a task.
func (s *Store) GetUnresolvedReviewComments(taskID string) ([]types.ReviewComment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE task_id = ? AND resolved = 0 ORDER BY file_path, line_num`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []types.ReviewComment{}
	for rows.Next() {
		var c types.ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// UpdateReviewComment updates a review comment's text and/or resolved status.
func (s *Store) UpdateReviewComment(id string, comment *string, resolved *bool) (*types.ReviewComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	query := `UPDATE review_comments SET updated_at = ?`
	args := []any{now.Format(time.RFC3339)}
	if comment != nil {
		query += `, comment = ?`
		args = append(args, *comment)
	}
	if resolved != nil {
		query += `, resolved = ?`
		args = append(args, b2i(*resolved))
	}
	query += ` WHERE id = ?`
	args = append(args, id)
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	// Return updated comment
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE id = ?`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		var c types.ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, fmt.Errorf("comment not found")
}

// DeleteReviewComment removes a review comment.
func (s *Store) DeleteReviewComment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM review_comments WHERE id = ?`, id)
	return err
}
