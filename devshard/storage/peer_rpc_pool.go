package storage

import "github.com/jackc/pgx/v5/pgxpool"

// PeerRPCPool returns the Postgres pool shared HA children use for peer RPC
// sessions. SQLite, memory, and a hybrid router with no Postgres backend
// return nil; those processes keep the in-memory session map.
func PeerRPCPool(s Storage) *pgxpool.Pool {
	for i := 0; i < 8 && s != nil; i++ {
		switch v := s.(type) {
		case *ObsRepairGate:
			if v == nil {
				return nil
			}
			s = v.Storage
		case *HybridStorage:
			if v == nil {
				return nil
			}
			if p := peerPoolOf(v.pg); p != nil {
				return p
			}
			return peerPoolOf(v.sqlite)
		case *Postgres:
			if v == nil {
				return nil
			}
			return v.pool
		default:
			return nil
		}
	}
	return nil
}

func peerPoolOf(s Storage) *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return PeerRPCPool(s)
}
