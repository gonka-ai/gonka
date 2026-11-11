package types

import (
	"bytes"
	"errors"
	"fmt"
)

// BeaconTxPrefix marks the injected beacon tx (Txs[0]): it is not an SDK tx.
var BeaconTxPrefix = []byte("GONKA_BEACON_V1")

// MaxBeaconTxBytes bounds a beacon tx before decoding. A proof of miss
// carries an extended commit (one vote and extension per validator).
const MaxBeaconTxBytes = 4 << 20

// IsBeaconTx reports whether tx carries the beacon prefix.
func IsBeaconTx(tx []byte) bool { return bytes.HasPrefix(tx, BeaconTxPrefix) }

// EncodeBeaconTx returns prefix ‖ proto(ib).
func EncodeBeaconTx(ib *InjectedBeacon) ([]byte, error) {
	if ib == nil || ib.Outcome == nil {
		return nil, errors.New("beacon tx: no outcome")
	}
	b, err := ib.Marshal()
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), BeaconTxPrefix...), b...), nil
}

// DecodeBeaconTx decodes a tx that carries the beacon prefix.
func DecodeBeaconTx(tx []byte) (*InjectedBeacon, error) {
	if !IsBeaconTx(tx) {
		return nil, errors.New("beacon tx: missing prefix")
	}
	if len(tx) > MaxBeaconTxBytes {
		return nil, fmt.Errorf("beacon tx: %d bytes over %d", len(tx), MaxBeaconTxBytes)
	}
	var ib InjectedBeacon
	if err := ib.Unmarshal(tx[len(BeaconTxPrefix):]); err != nil {
		return nil, fmt.Errorf("beacon tx: decode: %w", err)
	}
	if ib.Outcome == nil {
		return nil, errors.New("beacon tx: no outcome")
	}
	return &ib, nil
}
