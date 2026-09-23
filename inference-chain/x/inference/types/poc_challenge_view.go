package types

func (m *OpenPoCChallenge) Target() string {
	return m.GetChallenge().GetTarget()
}

func (m *OpenPoCChallenge) StartHeight() int64 {
	return m.GetChallenge().GetStartHeight()
}

func (m *OpenPoCChallenge) Seed() []byte {
	return m.GetChallenge().GetSeed()
}
