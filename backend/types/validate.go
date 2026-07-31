package types

import "fmt"

// ValidateBranchName ensures a branch name does not start with a hyphen,
// which would allow Git argument injection (e.g., "-f" interpreted as a flag).
func ValidateBranchName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("branch name is empty")
	}
	if name[0] == '-' {
		return fmt.Errorf("branch name must not start with a hyphen")
	}
	return nil
}

// ValidateTaskID ensures a task ID does not start with a hyphen,
// which would allow Git argument injection when the ID is used in branch names.
func ValidateTaskID(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("task ID is empty")
	}
	if id[0] == '-' {
		return fmt.Errorf("task ID must not start with a hyphen")
	}
	return nil
}
