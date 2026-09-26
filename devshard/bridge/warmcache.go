package bridge

import "sync"

// maxWarmKeyNegatives bounds the number of negative warm-key answers kept.
const maxWarmKeyNegatives = 4096

// WarmKeyCache memoises warm-key lookups for VerifyWarmKey.
//
// Positive answers are kept for the process lifetime, keyed by
// {granter, warm}, as before; they exist only for real on-chain grants.
// Negative answers are kept as before too, but at most maxWarmKeyNegatives of
// them: the payload route reaches this lookup with a caller-supplied address
// before it verifies the signature, and an unbounded negative map grew by one
// entry per group slot for every fresh address. When the negative set is full
// it is dropped, so an evicted pair is simply queried again: its answer can
// only become fresher, never staler.
type WarmKeyCache struct {
	mu      sync.Mutex
	granted map[warmPair]struct{}
	denied  map[warmPair]struct{}
}

type warmPair struct {
	granter string
	warm    string
}

// Verify reports whether warm is a grantee of granter. fetch returns the
// granter's current grantee addresses from the chain.
func (c *WarmKeyCache) Verify(warm, granter string, fetch func(granter string) ([]string, error)) (bool, error) {
	pair := warmPair{granter: granter, warm: warm}
	c.mu.Lock()
	_, ok := c.granted[pair]
	_, no := c.denied[pair]
	c.mu.Unlock()
	if ok {
		return true, nil
	}
	if no {
		return false, nil
	}

	addrs, err := fetch(granter)
	if err != nil {
		return false, err
	}
	found := false
	for _, a := range addrs {
		if a == warm {
			found = true
			break
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if found {
		if c.granted == nil {
			c.granted = make(map[warmPair]struct{})
		}
		c.granted[pair] = struct{}{}
		return true, nil
	}
	if c.denied == nil || len(c.denied) >= maxWarmKeyNegatives {
		c.denied = make(map[warmPair]struct{})
	}
	c.denied[pair] = struct{}{}
	return false, nil
}
