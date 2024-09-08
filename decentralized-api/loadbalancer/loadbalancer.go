package loadbalancer

import (
	"math/rand"
	"sync"
)

type LoadBalancer struct {
	participants    []participant
	cumulativeArray []int64
	mux             sync.RWMutex
}

type participant struct {
	id     string
	weight int64
}

func NewLoadBalancer(participants []participant) *LoadBalancer {
	lb := &LoadBalancer{
		participants: participants,
	}
	lb.computeCumulativeArray()
	return lb
}

func (lb *LoadBalancer) UpdateParticipants(participants []participant) {
	lb.mux.Lock()
	defer lb.mux.Unlock()

	lb.participants = participants
	lb.computeCumulativeArray()
}

func (lb *LoadBalancer) SelectRandomParticipant() string {
	lb.mux.RLock()
	defer lb.mux.RUnlock()

	if lb.cumulativeArray == nil {
		lb.computeCumulativeArray()
	}

	randomNumber := rand.Int63n(lb.cumulativeArray[len(lb.cumulativeArray)-1])
	for i, cumulativeWeight := range lb.cumulativeArray {
		if randomNumber < cumulativeWeight {
			return lb.participants[i].id
		}
	}
	return lb.participants[len(lb.participants)-1].id
}

func (lb *LoadBalancer) computeCumulativeArray() {
	cumulativeArray := make([]int64, len(lb.participants))
	cumulativeArray[0] = lb.participants[0].weight
	for i := 1; i < len(lb.participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + lb.participants[i].weight
	}
}
