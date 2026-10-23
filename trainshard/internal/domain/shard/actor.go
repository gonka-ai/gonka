package shard

import "trainshard/internal/domain/shared/vo"

type Actor struct {
	Address vo.Address
}

func (a Actor) AuthorizedFor(s Shard) bool {
	if a.Address == "" {
		return false
	}
	return a.Address == s.Creator || (s.RunKey != "" && a.Address == s.RunKey)
}
