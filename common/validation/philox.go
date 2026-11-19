package validation

import "math"

// torch draws the exponential noise of a request with its own seed from a CUDA
// Philox4x32-10 generator, one launch per sampling step: ceil(vocab/256) blocks
// of 256 threads, thread i writing the four values of the block (step, 0, i, 0)
// under the key (seed_lo, seed_hi) to elements i, i+stride, i+2*stride,
// i+3*stride, stride = 256 * blocks. vLLM samples argmax p_i / q_i over the
// kept tokens, so the value is a public function of seed, step and token, and a
// validator recomputes it here. torch's last step is a fast-math logarithm, so
// the values differ from its own in the last bits, and the launch needs a GPU
// that holds all blocks at once, which every datacenter GPU does.

const (
	philoxM0 = 0xD2511F53
	philoxM1 = 0xCD9E8D57
	philoxW0 = 0x9E3779B9
	philoxW1 = 0xBB67AE85
)

func philox4x32x10(c0, c1, c2, c3, k0, k1 uint32) [4]uint32 {
	for round := 0; round < 10; round++ {
		p0 := uint64(philoxM0) * uint64(c0)
		p1 := uint64(philoxM1) * uint64(c2)
		c0, c1, c2, c3 = uint32(p1>>32)^c1^k0, uint32(p1), uint32(p0>>32)^c3^k1, uint32(p0)
		k0 += philoxW0
		k1 += philoxW1
	}
	return [4]uint32{c0, c1, c2, c3}
}

// TorchExponential is the value torch's seeded CUDA exponential_ writes at index
// token of a vocab-long row on draw number step of a generator seeded with seed.
func TorchExponential(seed uint64, step uint32, vocab, token int) float64 {
	stride := 256 * ((vocab + 255) / 256)
	words := philox4x32x10(step, 0, uint32(token%stride), 0, uint32(seed), uint32(seed>>32))
	// curand_uniform: (0, 1], the conversions keep the float32 roundings apart.
	u := float32(float32(words[token/stride])*float32(2.3283064365386963e-10)) + float32(1.1641532182693481e-10)
	const eps = float32(5.960464477539063e-08)
	if u >= 1-eps/2 {
		return float64(eps / 2)
	}
	return -math.Log(float64(u))
}

// TapeNoise is the Gumbel noise the tape adds to the logit of token at output
// position step: -log of the exponential draw.
func TapeNoise(seed uint64, step uint32, vocab, token int) float64 {
	return -math.Log(TorchExponential(seed, step, vocab, token))
}
