package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A passkey row is one registered WebAuthn credential. There is no user table: one install has one
// operator, so a row is a device, not an account.
//
// `record` is the verifier's own credential record, serialized by the package that verifies the
// signature. It is a blob on purpose. The verifier needs more of it than the credential id and the key —
// the latched flag bits, the attestation type and format, and the raw attestation object it says to keep
// for re-verification — and a column per field is a list that has to keep agreeing with another
// package's struct. An assertion writes the record back because the flag bits and the signature counter
// move. The columns beside it are only what MIKE queries or owns: the id and relying-party id a ceremony
// selects by, the label a person typed, and the two instants.
//
// No index on rp_id: the table holds one row per device of one operator, and scanning that is cheaper
// than keeping a second b-tree in step.
const passkeyTable = `
CREATE TABLE IF NOT EXISTS passkeys (
    credential_id TEXT PRIMARY KEY,
    rp_id         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    record        BLOB NOT NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);
`

// initPasskeys creates the passkeys table. Like the other credential tables it is created by the package
// that reads it, not by baseSchemaSQL.
func (s *Store) initPasskeys() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applySchema(passkeyTable)
}

// NewPasskey carries the fields of a passkeys row. CredentialID is the authenticator's credential id in
// the base64url form the wire uses, because it is only ever selected by equality.
type NewPasskey struct {
	CredentialID string
	RPID         string
	Name         string
	Record       []byte
	// CreatedAt is when the credential was registered; the zero value means now.
	CreatedAt time.Time
}

// CreatePasskey registers a credential. Registering the same credential id twice fails on the primary
// key, which is the point: one credential's counter and flag state must live in exactly one row.
func (s *Store) CreatePasskey(ctx context.Context, p NewPasskey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt := p.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO passkeys (credential_id, rp_id, name, record, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.CredentialID, p.RPID, p.Name, p.Record, createdAt.Unix())
	if err != nil {
		return fmt.Errorf("create passkey: %w", err)
	}
	return nil
}

// PasskeyInfo is one passkey row for the management UI. It carries no credential record: a list of
// devices has no use for the bytes that verify a signature, and a view without them cannot leak one.
type PasskeyInfo struct {
	CredentialID string
	Name         string
	RPID         string
	CreatedAt    time.Time
	LastUsedAt   time.Time // zero when the credential has never signed anything
}

// ListPasskeys returns every registered credential, oldest first — the order a person reads a device list.
func (s *Store) ListPasskeys(ctx context.Context) ([]PasskeyInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT credential_id, rp_id, name, created_at, COALESCE(last_used_at, 0)
		   FROM passkeys ORDER BY created_at, credential_id`)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	passkeys, err := scanRows(rows, scanPasskeyRow)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	return passkeys, nil
}

func scanPasskeyRow(r *sql.Rows) (PasskeyInfo, error) {
	var (
		id, rpID, name string
		createdAt      int64
		usedAt         int64
	)
	if err := r.Scan(&id, &rpID, &name, &createdAt, &usedAt); err != nil {
		return PasskeyInfo{}, fmt.Errorf("scan passkey row: %w", err)
	}
	info := PasskeyInfo{
		CredentialID: id,
		RPID:         rpID,
		Name:         name,
		CreatedAt:    time.Unix(createdAt, 0),
	}
	// last_used_at 0 means never, which is a zero time rather than the epoch.
	if usedAt > 0 {
		info.LastUsedAt = time.Unix(usedAt, 0)
	}
	return info, nil
}

// PasskeyRecord is one stored credential for a ceremony: the id to offer and the record to verify with.
// Rows are selected by rp_id because a credential registered for one host cannot assert for another — the
// client data names the origin the ceremony ran on — so offering a foreign credential spends a prompt on
// a signature that must fail.
type PasskeyRecord struct {
	CredentialID string
	Record       []byte
}

// ListPasskeyRecords returns the credentials registered for one relying-party id.
func (s *Store) ListPasskeyRecords(ctx context.Context, rpID string) ([]PasskeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT credential_id, record FROM passkeys WHERE rp_id = ? ORDER BY created_at, credential_id`, rpID)
	if err != nil {
		return nil, fmt.Errorf("list passkey records: %w", err)
	}
	records, err := scanRows(rows, scanPasskeyRecordRow)
	if err != nil {
		return nil, fmt.Errorf("list passkey records: %w", err)
	}
	return records, nil
}

func scanPasskeyRecordRow(r *sql.Rows) (PasskeyRecord, error) {
	var rec PasskeyRecord
	if err := r.Scan(&rec.CredentialID, &rec.Record); err != nil {
		return PasskeyRecord{}, fmt.Errorf("scan passkey record row: %w", err)
	}
	return rec, nil
}

// PasskeyCount reports how many credentials are registered. Zero is the unpaired state: no session can
// survive a reset to it (ResetPasskeys takes the sessions too), so a person holding a browser and a
// pairing code is the only way in.
func (s *Store) PasskeyCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count passkeys: %w", err)
	}
	return n, nil
}

// RenamePasskey sets a credential's label. Returns false when no row has that id.
func (s *Store) RenamePasskey(ctx context.Context, credentialID, name string) (bool, error) {
	if credentialID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE passkeys SET name = ? WHERE credential_id = ?`, name, credentialID)
	if err != nil {
		return false, fmt.Errorf("rename passkey: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UsePasskey records a verified ceremony: the credential record moves (the signature counter and the
// latched user-verification bit), and so does last_used_at.
func (s *Store) UsePasskey(ctx context.Context, credentialID string, record []byte) error {
	if credentialID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `UPDATE passkeys SET record = ?, last_used_at = ? WHERE credential_id = ?`,
		record, time.Now().Unix(), credentialID); err != nil {
		return fmt.Errorf("use passkey: %w", err)
	}
	return nil
}

// ErrLastPasskey refuses to delete the only credential an install has. Every live session can mint a
// pairing code, so a GUI that could reach zero passkeys could leave a running server that nothing can sign
// in to and only `mike --reset-passkeys` can repair.
var ErrLastPasskey = errors.New("passkey is the only one registered")

// DeletePasskey revokes one credential and reports whether the row was there. Deleting the last remaining
// one is refused with ErrLastPasskey; see its comment. The count and the delete share one transaction so
// the refusal cannot be decided from a number the next writer has already changed.
func (s *Store) DeletePasskey(ctx context.Context, credentialID string) (bool, error) {
	if credentialID == "" {
		return false, nil
	}
	var deleted bool
	err := s.tx(func(tx *sql.Tx) error {
		var known int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM passkeys WHERE credential_id = ?`, credentialID).Scan(&known); err != nil {
			return fmt.Errorf("count passkeys: %w", err)
		}
		if known == 0 {
			return nil
		}
		var total int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM passkeys`).Scan(&total); err != nil {
			return fmt.Errorf("count passkeys: %w", err)
		}
		if total == 1 {
			return ErrLastPasskey
		}
		res, err := tx.Exec(`DELETE FROM passkeys WHERE credential_id = ?`, credentialID)
		if err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		deleted = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// ResetPasskeys is what `mike --reset-passkeys` does: every credential and every session go together, in
// one transaction. Sessions go because a live session can mint a pairing code, so a reset that left them
// running would hand the lost device the means to register a new credential behind the reset.
func (s *Store) ResetPasskeys(ctx context.Context) (passkeys, sessions int, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		p, err := tx.Exec(`DELETE FROM passkeys`)
		if err != nil {
			return fmt.Errorf("delete passkeys: %w", err)
		}
		n, err := p.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete passkeys: %w", err)
		}
		passkeys = int(n)
		ses, err := tx.Exec(`DELETE FROM sessions`)
		if err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		n, err = ses.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		sessions = int(n)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return passkeys, sessions, nil
}
