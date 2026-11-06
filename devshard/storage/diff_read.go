package storage

import (
	"devshard/types"
)

// diffScanner is the row cursor shared by database/sql and pgx.
type diffScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// readDiffRecords scans one stored diff per row. The query must not join
// signatures: that repeats txs_proto once per signature.
func readDiffRecords(rows diffScanner) ([]types.DiffRecord, error) {
	var recs []types.DiffRecord
	for rows.Next() {
		var nonce uint64
		var txsProto, userSig, postStateRoot, stateHash []byte
		var createdAt int64
		if err := rows.Scan(&nonce, &txsProto, &userSig, &postStateRoot, &stateHash, &createdAt); err != nil {
			return nil, err
		}
		rec, err := diffRecordFromStored(nonce, txsProto, userSig, postStateRoot, stateHash, createdAt)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, rows.Err()
}

func diffRecordFromStored(nonce uint64, txsProto, userSig, postStateRoot, stateHash []byte, createdAt int64) (types.DiffRecord, error) {
	txs, err := unmarshalTxs(txsProto)
	if err != nil {
		return types.DiffRecord{}, err
	}
	rec := types.DiffRecord{
		Diff: types.Diff{
			Nonce:         nonce,
			Txs:           txs,
			UserSig:       userSig,
			PostStateRoot: postStateRoot,
		},
		StateHash: stateHash,
		CreatedAt: createdAt,
	}
	return rec, nil
}

// attachDiffSignatureRows adds signature rows onto recs. rows must be ordered
// by nonce. A signature whose nonce has no diff is ignored, matching a join
// that starts from diffs.
func attachDiffSignatureRows(rows diffScanner, recs []types.DiffRecord) error {
	idx := 0
	for rows.Next() {
		var nonce uint64
		var slotID uint32
		var sig []byte
		if err := rows.Scan(&nonce, &slotID, &sig); err != nil {
			return err
		}
		for idx < len(recs) && recs[idx].Nonce < nonce {
			idx++
		}
		if idx >= len(recs) || recs[idx].Nonce != nonce {
			continue
		}
		if recs[idx].Signatures == nil {
			recs[idx].Signatures = make(map[uint32][]byte)
		}
		recs[idx].Signatures[slotID] = sig
	}
	return rows.Err()
}
