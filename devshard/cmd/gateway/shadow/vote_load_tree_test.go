package shadow_test

import (
	"devshard/cmd/gateway/warmup"
)

const treeName = "mine"

func newTreeVoter(posters warmup.Posters, clock *virtualClock) warmup.Voter {
	return warmup.Voter{Posters: posters, Pace: warmup.NewVotePacer(clock.now, clock.sleep)}
}

func peakBound(int) int { return warmup.VoteRoundBurst + int(warmup.VoteRoundsPerSecond) }
