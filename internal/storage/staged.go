package storage

import (
	"os"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Staged owns only bytes and prepared metadata. The command coordinator transfers
// file ownership with the committed record; no transaction can invoke this handle.
type Staged struct {
	Blob database.Blob
	path string
}

func (s *Staged) Commit(blob database.Blob) {
	s.Blob = blob
	s.path = ""
}

func (s *Staged) Discard() {
	if s.path != "" {
		os.Remove(s.path)
		s.path = ""
	}
}
