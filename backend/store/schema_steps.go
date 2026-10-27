package store

import (
	"database/sql"
	"fmt"
	"strings"

	"mike/pkg/logging"
)

// A schemaStep changes stored data in a way a column declaration cannot express. It runs at most once per
// file. The rule is in docs/storage.md.
type schemaStep struct {
	// Key names the step and the marker row it leaves behind. Keys are dated and registered oldest first.
	Key string
	// OneWay marks a step that destroys data an older build reads, so an older build refuses the file.
	// Nothing sets it yet, and the check has to ship before the step that sets it.
	OneWay bool
	// Apply does the work. It runs at most once per file, after the shape pass, and it owns its
	// transaction, so a failure leaves no marker row and the next boot tries again.
	Apply func(*Store) error
}

// schemaSteps is the ladder. The shape pass adds everything an older file can be missing that this release
// reads, so this list holds data rewrites only.
var schemaSteps = []schemaStep{
	{
		Key:   "2026-10-26-tasks-null-to-blank",
		Apply: fillBlankWhereNull,
	},
}

// runSchemaSteps runs the steps this file has not recorded. A step that fails stops the boot and names
// itself, so no board is served over partly moved data.
func (s *Store) runSchemaSteps(steps []schemaStep) error {
	for _, step := range steps {
		done, err := s.stepDone(step.Key)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		if err := step.Apply(s); err != nil {
			return fmt.Errorf("schema step %s: %w", step.Key, err)
		}
		if err := s.markStepDone(step.Key, step.OneWay); err != nil {
			return err
		}
		logging.For(logging.Store).Infof("applied schema step %q", step.Key)
	}
	return nil
}

// checkSchemaStepsAhead looks for steps this build has never heard of. An unknown step that only added data
// warns, because an older build ignores a column it does not know. An unknown step recorded as one-way
// refuses the boot, because its old bytes are gone.
func (s *Store) checkSchemaStepsAhead(steps []schemaStep) error {
	known := map[string]bool{}
	for _, step := range steps {
		known[step.Key] = true
	}
	keys, err := s.markerKeys(stepRowPrefix)
	if err != nil {
		return err
	}
	var ahead, oneWay []string
	for _, key := range keys {
		name := strings.TrimPrefix(key, stepRowPrefix)
		if known[name] {
			continue
		}
		ahead = append(ahead, name)
		_, isOneWay, err := s.metaValue(oneWayRowPrefix + name)
		if err != nil {
			return err
		}
		if isOneWay {
			oneWay = append(oneWay, name)
		}
	}
	if len(oneWay) > 0 {
		return fmt.Errorf("schema upgrade refused: step %s rewrote data this build cannot read, and this build does not "+
			"have that step. Run the build that recorded it", strings.Join(oneWay, ", "))
	}
	if len(ahead) > 0 {
		logging.For(logging.Store).Warnf("this database has been through step %s, which this build does not know. That step "+
			"only added data, so the boot continues.", strings.Join(ahead, ", "))
	}
	return nil
}

// stepDone reports whether this file recorded the step.
func (s *Store) stepDone(key string) (bool, error) {
	_, found, err := s.metaValue(stepRowPrefix + key)
	return found, err
}

// markStepDone records the step and whether an older build may still read the file. Both rows go in one
// transaction, so a file is never marked done without its one-way row.
func (s *Store) markStepDone(key string, oneWay bool) error {
	rows := [][2]string{{stepRowPrefix + key, stepRowValue}}
	if oneWay {
		rows = append(rows, [2]string{oneWayRowPrefix + key, stepRowValue})
	}
	return s.tx(func(tx *sql.Tx) error {
		for _, row := range rows {
			if err := setMetaRow(tx, row[0], row[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// nullToBlankColumns are task columns an older build added without a default, so existing rows got NULL.
// scanTask reads all three as strings, so one such row failed every board read.
var nullToBlankColumns = []string{"session_id", "branch", "output"}

// fillBlankWhereNull writes an empty string into those columns. It is not one-way, because an older build
// reads an empty string where it could not scan a NULL.
func fillBlankWhereNull(s *Store) error {
	filled := 0
	err := s.tx(func(tx *sql.Tx) error {
		for _, column := range nullToBlankColumns {
			// A column name cannot be a bound parameter, and these names are fixed in this file.
			res, err := tx.Exec(`UPDATE tasks SET ` + column + ` = '' WHERE ` + column + ` IS NULL`)
			if err != nil {
				return fmt.Errorf("blank out NULL %s: %w", column, err)
			}
			rows, _ := res.RowsAffected()
			filled += int(rows)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if filled > 0 {
		logging.For(logging.Store).Infof("blanked %d task row values that arrived as NULL", filled)
	}
	return nil
}
