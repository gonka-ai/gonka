package store

import "time"

// FormatTime renders every stored timestamp and the accounting API's, so the retention cutoff cannot drift from the rows it compares. See README.md, "Timestamps".
func FormatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(storedTimeLayout)
}

// ParseTime is the only reader of what FormatTime writes, the empty zero timestamp included.
func ParseTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}

// SettleStamp renders the process-clock time recorded with a settle hash; an empty hash clears it.
func SettleStamp(txHash string, now time.Time) string {
	if txHash == "" {
		return ""
	}
	return now.UTC().Format(settleStampLayout)
}

// ParseSettleStamp reads a SettleStamp; a stamp that does not parse is the zero time.
func ParseSettleStamp(raw string) time.Time {
	parsed, err := time.Parse(settleStampLayout, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
