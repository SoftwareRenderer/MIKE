package config

import "fmt"

// Resetting a project's cumulative token counters is one route: `DELETE /api/projects/{id}/stats`.
// Deleting a task or project zeroes rows by a different path, which this lock does not cover.

// EnvRestrictStatReset names the operator switch. backend/startup holds the copy the ladder reads;
// startup.TestEnvNamesMatchTheirOwners holds the two together.
const EnvRestrictStatReset = "MIKE_RESTRICT_STAT_RESET"

// ErrStatsResetLocked names the variable and what remains writable, so the person refused
// knows the counters are not broken.
var ErrStatsResetLocked = fmt.Errorf(
	"token stats are protected from reset by %s: a model's usage counters cannot be cleared from Settings, "+
		"while usage keeps being recorded and deleting a task or project still removes the rows it owns", EnvRestrictStatReset)

func (cs *ConfigStore) StatResetLocked() bool { return cs.statResetLocked }
