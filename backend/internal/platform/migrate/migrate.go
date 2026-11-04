// Package migrate applies SQL migrations (golang-migrate with the pgx/v5 driver).
package migrate

import (
	"errors"
	"fmt"
	"strings"

	gm "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func newMigrate(dbURL, dir string) (*gm.Migrate, error) {
	u := dbURL
	switch {
	case strings.HasPrefix(u, "postgres://"):
		u = "pgx5://" + strings.TrimPrefix(u, "postgres://")
	case strings.HasPrefix(u, "postgresql://"):
		u = "pgx5://" + strings.TrimPrefix(u, "postgresql://")
	}
	return gm.New("file://"+dir, u)
}

// Up applies all pending migrations. "No change" is not an error.
func Up(dbURL, dir string) error {
	m, err := newMigrate(dbURL, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, gm.ErrNoChange) {
		return err
	}
	return nil
}

// Down rolls back n migrations (n > 0). It is never called automatically by services.
func Down(dbURL, dir string, n int) error {
	if n <= 0 {
		return fmt.Errorf("migrate: down requires n > 0")
	}
	m, err := newMigrate(dbURL, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Steps(-n); err != nil && !errors.Is(err, gm.ErrNoChange) {
		return err
	}
	return nil
}

// DownAll rolls back everything (tests only).
func DownAll(dbURL, dir string) error {
	m, err := newMigrate(dbURL, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, gm.ErrNoChange) {
		return err
	}
	return nil
}

// Version returns the current version and the dirty flag (0 if nothing is applied).
func Version(dbURL, dir string) (uint, bool, error) {
	m, err := newMigrate(dbURL, dir)
	if err != nil {
		return 0, false, err
	}
	defer m.Close()
	v, dirty, err := m.Version()
	if errors.Is(err, gm.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}
