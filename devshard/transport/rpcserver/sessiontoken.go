package rpcserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	// SessionTokenVersion is the first byte of a peer RPC session token.
	SessionTokenVersion = 1
	// SessionKeyID selects the HMAC key. Bumping it logs every peer out.
	SessionKeyID = 1

	sessionTokenFieldMax = 128
	// maxSessionTokenBytes bounds the bearer. It is larger than an attach
	// nonce because the token carries the peer, host, and version.
	maxSessionTokenBytes = 512
	// sessionTokenSkew is how far a verifying child's clock may run ahead
	// of the issuing child's. Both are children of one host, so this covers
	// NTP drift between replicas only. Client clock drift is already bounded
	// at Attach by transport.MaxTimestampDrift and does not extend expiry.
	sessionTokenSkew = 5 * time.Second
)

var (
	errSessionToken   = errors.New("invalid session token")
	errSessionExpired = errors.New("session expired")
)

// issueSessionToken builds a token that any replica with the same key can
// check. attachedUnix is the earlier of the Attach timestamp and the host
// clock, so the same past-dated request and TTL produce the same bytes.
func issueSessionToken(key []byte, keyID byte, host, version, peer string, attachedUnix int64, ttl time.Duration, nonce []byte) ([]byte, time.Time, error) {
	if len(key) == 0 {
		return nil, time.Time{}, fmt.Errorf("session token: key is required")
	}
	exp := time.Unix(attachedUnix, 0).UTC().Add(ttl)
	body, err := sessionTokenBody(keyID, host, version, peer, attachedUnix, exp.Unix(), nonce)
	if err != nil {
		return nil, time.Time{}, err
	}
	mac := sessionTokenMAC(key, body)
	return append(body, mac...), exp, nil
}

// openSessionToken checks the tag, then host, version, key id, and expiry.
// skew is how far the verifier's clock may sit behind the expiry instant.
func openSessionToken(key []byte, keyID byte, host, version string, token []byte, now time.Time, skew time.Duration) (peer string, exp time.Time, expired bool, err error) {
	if len(key) == 0 || len(token) < 2+sha256.Size || len(token) > maxSessionTokenBytes {
		return "", time.Time{}, false, errSessionToken
	}
	body := token[:len(token)-sha256.Size]
	mac := token[len(token)-sha256.Size:]
	if !hmac.Equal(mac, sessionTokenMAC(key, body)) {
		return "", time.Time{}, false, errSessionToken
	}
	if body[0] != SessionTokenVersion || body[1] != keyID {
		return "", time.Time{}, false, errSessionToken
	}
	fields, rest, err := takeStrings(body[2:], 3)
	if err != nil || len(rest) != 8+8+sha256.Size {
		return "", time.Time{}, false, errSessionToken
	}
	if fields[0] != host || fields[1] != version {
		return "", time.Time{}, false, errSessionToken
	}
	expUnix := int64(binary.BigEndian.Uint64(rest[8:16]))
	exp = time.Unix(expUnix, 0).UTC()
	if now.After(exp.Add(skew)) {
		return fields[2], exp, true, errSessionExpired
	}
	return fields[2], exp, false, nil
}

func sessionTokenBody(keyID byte, host, version, peer string, attachedUnix, expUnix int64, nonce []byte) ([]byte, error) {
	buf := []byte{SessionTokenVersion, keyID}
	var err error
	for _, field := range []string{host, version, peer} {
		buf, err = appendSessionString(buf, field)
		if err != nil {
			return nil, err
		}
	}
	var nums [16]byte
	binary.BigEndian.PutUint64(nums[0:8], uint64(attachedUnix))
	binary.BigEndian.PutUint64(nums[8:16], uint64(expUnix))
	buf = append(buf, nums[:]...)
	sum := sha256.Sum256(nonce)
	buf = append(buf, sum[:]...)
	return buf, nil
}

func sessionTokenMAC(key, body []byte) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(body)
	return m.Sum(nil)
}

func appendSessionString(buf []byte, s string) ([]byte, error) {
	if len(s) > sessionTokenFieldMax {
		return nil, fmt.Errorf("session token: field longer than %d bytes", sessionTokenFieldMax)
	}
	var n [2]byte
	binary.BigEndian.PutUint16(n[:], uint16(len(s)))
	buf = append(buf, n[:]...)
	buf = append(buf, s...)
	return buf, nil
}

func takeStrings(buf []byte, n int) ([]string, []byte, error) {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if len(buf) < 2 {
			return nil, nil, errSessionToken
		}
		size := int(binary.BigEndian.Uint16(buf[:2]))
		buf = buf[2:]
		if size > sessionTokenFieldMax || len(buf) < size {
			return nil, nil, errSessionToken
		}
		out = append(out, string(buf[:size]))
		buf = buf[size:]
	}
	return out, buf, nil
}
