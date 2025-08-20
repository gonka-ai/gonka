package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// InferenceValidationDetailsKeyPrefix is the prefix to retrieve all InferenceValidationDetails
	InferenceValidationDetailsKeyPrefix = "InferenceValidationDetails/value/"
)
