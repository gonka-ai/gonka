package blocks

// DummyHeader is what At() returns when GET /block/:height is missing
// (0.2.15 dapi). Height is the requested lookup; Time is the zero value;
// BlockHash is empty. L6 treats this as "no oracle view" and does not mark.
func DummyHeader(height int64) *Header {
	return &Header{Height: height}
}

// IsDummyHeader reports a placeholder from DummyHeader: zero time and no hash.
func IsDummyHeader(h *Header) bool {
	return h != nil && h.Time.IsZero() && len(h.BlockHash) == 0
}
