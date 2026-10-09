package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// CreatePairingCode stores a single-use pairing code valid for ttl and returns it.
func (s *Store) CreatePairingCode(ctx context.Context, ttl time.Duration) (string, error) {
	code := RandomToken(16)
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM pairing_codes WHERE expires_at < ?`, now.Unix())
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO pairing_codes(code_hash, expires_at) VALUES(?, ?)`,
		hashSecret(code), now.Add(ttl).Unix())
	return code, err
}

var ErrBadPairingCode = errors.New("invalid or expired pairing code")

// RedeemPairingCode consumes code and registers a new device, returning its token.
func (s *Store) RedeemPairingCode(ctx context.Context, code, deviceName string) (Device, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, "", err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes WHERE code_hash=? AND expires_at >= ?`,
		hashSecret(code), time.Now().Unix())
	if err != nil {
		return Device{}, "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Device{}, "", ErrBadPairingCode
	}
	if deviceName == "" {
		deviceName = "device"
	}
	d := Device{ID: NewID(), Name: deviceName, CreatedAt: time.Now().UTC()}
	token := RandomToken(32)
	_, err = tx.ExecContext(ctx, `INSERT INTO devices(id, name, token_hash, created_at) VALUES(?, ?, ?, ?)`,
		d.ID, d.Name, hashSecret(token), d.CreatedAt.Unix())
	if err != nil {
		return Device{}, "", err
	}
	return d, token, tx.Commit()
}

// AddDevice registers a device directly (used by `rad debug`), returning its token.
func (s *Store) AddDevice(ctx context.Context, name string) (Device, string, error) {
	code, err := s.CreatePairingCode(ctx, time.Minute)
	if err != nil {
		return Device{}, "", err
	}
	return s.RedeemPairingCode(ctx, code, name)
}

// DeviceByToken authenticates a bearer token.
func (s *Store) DeviceByToken(ctx context.Context, token string) (Device, error) {
	var d Device
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM devices WHERE token_hash=?`,
		hashSecret(token)).Scan(&d.ID, &d.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.CreatedAt = time.Unix(created, 0).UTC()
	return d, err
}

func (s *Store) TouchDevice(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) DeviceExists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_seen_at FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created int64
		var seen sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(created, 0).UTC()
		if seen.Valid {
			t := time.Unix(seen.Int64, 0).UTC()
			d.LastSeenAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

var ErrAmbiguousPrefix = errors.New("id prefix matches more than one device")

// DeleteDevice revokes the device whose id starts with idPrefix. Device ids
// are time-ordered (UUIDv7), so devices paired close together share long
// prefixes; an ambiguous prefix revokes nothing.
func (s *Store) DeleteDevice(ctx context.Context, idPrefix string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const match = `substr(id, 1, length(?)) = ?` // not LIKE: '_' and '%' are wildcards there
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE `+match, idPrefix, idPrefix).Scan(&n); err != nil {
		return 0, err
	}
	if n > 1 {
		return 0, ErrAmbiguousPrefix
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE `+match, idPrefix, idPrefix); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
