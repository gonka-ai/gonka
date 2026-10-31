package adminface

import (
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"

	"devshard/testenv/gatewayphase"
	"devshard/testenv/mockchain/store"
)

// PhaseRequest moves the chain's reported epoch phase and says which participants survive it. Absent
// fields are left as they were, so a scenario can set the phase without restating the group.
type PhaseRequest struct {
	Phase                 *string  `json:"phase,omitempty"`
	ConfirmationPoCActive *bool    `json:"confirmation_poc_active,omitempty"`
	PreservedAddresses    []string `json:"preserved_addresses,omitempty"`
	PoCWeight             *uint64  `json:"poc_weight,omitempty"`
	// PublishParticipants turns the participants response on. Left off, the group stays empty, which is
	// what every stand saw before this route existed.
	PublishParticipants *bool `json:"publish_participants,omitempty"`
}

// phaseOverride is what POST /testenv/phase last asked for, read on every public-API request.
type phaseOverride struct {
	mu                    sync.RWMutex
	phase                 string
	confirmationPoCActive bool
	preserved             map[string]bool
	pocWeight             uint64
	publish               bool
}

func (o *phaseOverride) apply(req PhaseRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if req.Phase != nil {
		o.phase = *req.Phase
	}
	if req.ConfirmationPoCActive != nil {
		o.confirmationPoCActive = *req.ConfirmationPoCActive
	}
	if req.PreservedAddresses != nil {
		o.preserved = make(map[string]bool, len(req.PreservedAddresses))
		for _, address := range req.PreservedAddresses {
			o.preserved[address] = true
		}
	}
	if req.PoCWeight != nil {
		o.pocWeight = *req.PoCWeight
	}
	if req.PublishParticipants != nil {
		o.publish = *req.PublishParticipants
	}
}

// read folds the override over the store's participants. A participant is preserved unless the scenario
// named a set that leaves it out, so publishing a group does not silently withhold everyone.
func (o *phaseOverride) read(st *store.Store) (string, bool, []gatewayphase.Participant) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if !o.publish {
		return o.phase, o.confirmationPoCActive, nil
	}
	weight := o.pocWeight
	if weight == 0 {
		weight = 1000
	}
	stored := st.ListParticipants()
	participants := make([]gatewayphase.Participant, 0, len(stored))
	for _, participant := range stored {
		preserved := true
		if o.preserved != nil {
			preserved = o.preserved[participant.Address]
		}
		participants = append(participants, gatewayphase.Participant{
			Address:      participant.Address,
			InferenceURL: participant.InferenceUrl,
			PoCWeight:    weight,
			Preserved:    preserved,
		})
	}
	return o.phase, o.confirmationPoCActive, participants
}

// MountPhase registers POST /testenv/phase.
func MountPhase(g *echo.Group, override *phaseOverride) {
	if g == nil || override == nil {
		return
	}
	g.POST("/testenv/phase", func(c echo.Context) error {
		var req PhaseRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		override.apply(req)
		return c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})
}
