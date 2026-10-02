package config

import "fmt"

// Resetting a project's cumulative token counters is one route: `DELETE /api/projects/{id}/stats`.
// Deleting a task or project zeroes rows by a different path, which this lock does not cover.

// EnvRestrictStatReset names the operator switch. backend/startup holds the copy the ladder reads;
// startup.TestEnvNamesMatchTheirOwners holds the two names together.
const EnvRestrictStatReset = "MIKE_RESTRICT_STAT_RESET"

// ErrStatsResetLocked also names what the lock leaves alone, so whoever is refused knows the
// counters are not broken.
var ErrStatsResetLocked = fmt.Errorf(
	"token stats are protected from reset by %s: a model's usage counters cannot be cleared from Settings, "+
		"while usage keeps being recorded and deleting a task or project still removes the rows it owns", EnvRestrictStatReset)

// StatResetLocked is the switch this store was built with.
func (cs *ConfigStore) StatResetLocked() bool { return cs.statResetLocked }
