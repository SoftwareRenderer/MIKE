package store

import (
	"errors"
	"fmt"

	"mike/backend/errs"
)

// Sentinels for the single-row readers. Each wraps errs.ErrNotFound with the entity's
// name, so the message is unchanged and errors.Is still classifies it - which lets
// httputil.Fail answer 404 for a missing row and 500 for a fault instead of treating any
// error as "not found" and showing an empty project where the database failed.
var (
	ErrTaskNotFound    = fmt.Errorf("task %w", errs.ErrNotFound)
	ErrProjectNotFound = fmt.Errorf("project %w", errs.ErrNotFound)
	ErrThreadNotFound  = fmt.Errorf("thread %w", errs.ErrNotFound)
	ErrMessageNotFound = fmt.Errorf("task message %w", errs.ErrNotFound)
)

// ErrMessageCursor is a paging cursor this store never issued. Refusing it beats an empty page, which a
// client would read as "no history".
var ErrMessageCursor = errors.New("malformed message cursor")
