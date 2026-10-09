package devshard

import (
	"errors"
	"fmt"
	"strconv"
)

var ErrInvalidEscrowID = errors.New("invalid escrow id")

func ParseEscrowID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w %q", ErrInvalidEscrowID, raw)
	}
	if id == 0 {
		return 0, fmt.Errorf("%w %q", ErrInvalidEscrowID, raw)
	}
	if canonical := strconv.FormatUint(id, 10); raw != canonical {
		return 0, fmt.Errorf("%w %q: canonical form is %q", ErrInvalidEscrowID, raw, canonical)
	}
	return id, nil
}

func ValidateEscrowID(raw string) error {
	_, err := ParseEscrowID(raw)
	return err
}
