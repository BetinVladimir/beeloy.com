// Package ids generates and parses entity identifiers (UUIDv7).
package ids

import "github.com/google/uuid"

// New returns a new time-ordered UUIDv7.
func New() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func Parse(s string) (uuid.UUID, error) { return uuid.Parse(s) }
