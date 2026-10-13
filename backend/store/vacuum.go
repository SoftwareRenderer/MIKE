package store

// Free-space reclamation. In the default mode a database keeps every page a delete emptied, so this file
// switches the file to incremental autovacuum and later truncates free pages off its end. The reasoning
// and the measurements: docs/storage.md.
//
// Incremental rather than full autovacuum, because full shuffles pages inside whichever write next finds
// free ones, which puts this cost on an unrelated insert.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"mike/pkg/logging"
)

// autoVacuumIncremental is PRAGMA auto_vacuum's incremental mode. A new file starts in none (0).
const autoVacuumIncremental = 2

const (
	// Free pages cost disk only, so this is housekeeping on a slow timer.
	vacuumSweepInterval = 5 * time.Minute
	// One truncation stops every query in the process, so it is not worth running for a few free pages.
	// 4096 pages is 16 MB at the page size this schema is created with.
	vacuumFloorPages = 4096
	// The most one truncation gives back. A full burst of this size holds the store lock for 43 ms here.
	vacuumBurstPages = 4096
)

// ensureIncrementalAutovacuum switches the file to incremental autovacuum. The pragma alone does nothing
// to a file that already has a header, so switching is the pragma followed by one VACUUM.
func ensureIncrementalAutovacuum(db *sql.DB) error {
	mode, err := readAutoVacuumMode(db)
	if err != nil {
		return err
	}
	if mode == autoVacuumIncremental {
		return nil
	}
	if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return fmt.Errorf("set autovacuum mode: %w", err)
	}
	started := time.Now()
	pagesBefore := pageCount(db)
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("convert autovacuum mode: %w", err)
	}
	// The rewrite reaches the disk only when the log is applied; see applyWAL.
	if err := applyWAL(db); err != nil {
		return err
	}
	mode, err = readAutoVacuumMode(db)
	if err != nil {
		return err
	}
	if mode != autoVacuumIncremental {
		return fmt.Errorf("convert autovacuum mode: the file still reports mode %d", mode)
	}
	// Logged only when it gave space back, since every test database converts.
	if pagesAfter := pageCount(db); pagesAfter > 0 && pagesAfter < pagesBefore {
		logging.For(logging.Store).Infof("converted the database to incremental autovacuum in %s, giving back %d free pages",
			time.Since(started).Round(time.Millisecond), pagesBefore-pagesAfter)
	}
	return nil
}

// StartVacuumSweep returns free pages to the operating system until ctx is cancelled. Cancel it before
// closing the store: a burst writes the file every reader shares.
func (s *Store) StartVacuumSweep(ctx context.Context) {
	go func() {
		tick := time.NewTicker(vacuumSweepInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			mode, err := s.autovacuumMode()
			if err != nil {
				logging.For(logging.Store).Warnf("reading the autovacuum mode failed: %v", err)
				continue
			}
			if mode != autoVacuumIncremental {
				// Nothing later changes a file's mode, so this is reported once rather than hourly.
				logging.For(logging.Store).Warnf("free pages will not be reclaimed: the file is in autovacuum mode %d", mode)
				return
			}
			freed, err := s.reclaimFreePages(vacuumFloorPages, vacuumBurstPages)
			if err != nil {
				logging.For(logging.Store).Warnf("reclaiming free pages failed: %v", err)
				continue
			}
			if freed > 0 {
				logging.For(logging.Store).Infof("returned %d free pages to the operating system", freed)
			}
		}
	}()
}

// reclaimFreePages truncates up to burstPages free pages and reports how many went, doing nothing below
// floorPages. Both ceilings are arguments so a test can drive them with a small database.
//
// One write statement, so no tx; the write lock, because truncating changes the file under every reader.
func (s *Store) reclaimFreePages(floorPages, burstPages int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Applied before anything is measured: pages freed inside an unapplied transaction are not in the
	// file's freelist yet.
	if err := applyWAL(s.db); err != nil {
		return 0, err
	}
	free, err := freelistCount(s.db)
	if err != nil {
		return 0, err
	}
	if free < int64(floorPages) {
		return 0, nil
	}
	// A PRAGMA takes no bound parameter, so this text is assembled. Only package constants and test
	// literals reach it.
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", burstPages)); err != nil {
		return 0, fmt.Errorf("incremental vacuum: %w", err)
	}
	// And applied again, so the shorter file reaches the disk.
	if err := applyWAL(s.db); err != nil {
		return 0, err
	}
	after, err := freelistCount(s.db)
	if err != nil {
		return 0, err
	}
	return free - after, nil
}

// applyWAL writes this connection's changes into the main file. SQLite checkpoints on its own only after
// about 1000 dirty pages, and a passive checkpoint never waits on a reader outside this process.
func applyWAL(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA wal_checkpoint"); err != nil {
		return fmt.Errorf("checkpoint the write-ahead log: %w", err)
	}
	return nil
}

// autovacuumMode reads the mode under the store lock, which a goroutine must not bypass.
func (s *Store) autovacuumMode() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readAutoVacuumMode(s.db)
}

// readAutoVacuumMode reads PRAGMA auto_vacuum: 0 none, 1 full, 2 incremental. It runs at boot, before a
// Store exists, and under s.mu from autovacuumMode.
func readAutoVacuumMode(db *sql.DB) (int64, error) {
	var mode int64
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return 0, fmt.Errorf("read autovacuum mode: %w", err)
	}
	return mode, nil
}

// freelistCount counts pages no row uses. The caller holds s.mu.
func freelistCount(db *sql.DB) (int64, error) {
	var free int64
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		return 0, fmt.Errorf("read freelist count: %w", err)
	}
	return free, nil
}

// pageCount reports how many pages the file is, or 0 when the file will not say. Only the conversion's log
// line reads it, and a zero there simply drops the line.
func pageCount(db *sql.DB) int64 {
	var pages int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return 0
	}
	return pages
}
