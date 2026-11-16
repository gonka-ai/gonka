package storage

// Flush resolving diffs before deleting journal entries.
func (s *SQLite) RequireDurableCommits(escrowID string) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	_, err = p.writeDB.Exec("PRAGMA synchronous=FULL")
	return err
}

func (s *HybridStorage) RequireDurableCommits(escrowID string) error {
	owner, err := s.backendFor(escrowID)
	if err != nil {
		return err
	}
	if durable, ok := owner.(interface{ RequireDurableCommits(string) error }); ok {
		return durable.RequireDurableCommits(escrowID)
	}
	return nil
}
