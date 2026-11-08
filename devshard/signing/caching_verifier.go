package signing

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

// CachingVerifier remembers the addresses it recovered. A gateway session
// checks the same executor receipt and the same Finish signature when the
// host answers, again as evidence, and again when the diff applies them; with
// the cache only the first check pays for a secp256k1 recovery. Recovery is a
// pure function of the message and the signature, so a remembered address is
// the one a fresh recovery would return. Failed recoveries are not kept.
type CachingVerifier struct {
	inner Verifier
	limit int

	mu    sync.Mutex
	byKey map[string]*list.Element
	order *list.List // front = least recently used
}

type recoveredAddress struct {
	key     string
	address string
}

// NewCachingVerifier wraps inner with an LRU of at most limit recoveries.
func NewCachingVerifier(inner Verifier, limit int) *CachingVerifier {
	return &CachingVerifier{
		inner: inner,
		limit: max(limit, 1),
		byKey: make(map[string]*list.Element),
		order: list.New(),
	}
}

func (v *CachingVerifier) RecoverAddress(message []byte, sig []byte) (string, error) {
	digest := sha256.Sum256(message)
	key := string(digest[:]) + string(sig)
	v.mu.Lock()
	if el, ok := v.byKey[key]; ok {
		v.order.MoveToBack(el)
		address := el.Value.(*recoveredAddress).address
		v.mu.Unlock()
		return address, nil
	}
	v.mu.Unlock()

	address, err := v.inner.RecoverAddress(message, sig)
	if err != nil {
		return "", err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if el, ok := v.byKey[key]; ok {
		v.order.MoveToBack(el)
		return address, nil
	}
	v.byKey[key] = v.order.PushBack(&recoveredAddress{key: key, address: address})
	for v.order.Len() > v.limit {
		oldest := v.order.Front()
		v.order.Remove(oldest)
		delete(v.byKey, oldest.Value.(*recoveredAddress).key)
	}
	return address, nil
}
