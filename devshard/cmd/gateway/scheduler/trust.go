package scheduler

import (
	"slices"
	"strings"

	"devshard/cmd/gateway/config"
)

// refusedByTrust stays nil when nobody narrowed routing, so the drain reads "no trust list" as no rung at all.
func refusedByTrust(trust, unthrottled []string) func(participant string) bool {
	if len(trust) == 0 {
		return nil
	}
	admitted := admittedParticipants(trust, unthrottled)
	return func(participant string) bool { return !admitted(participant) }
}

// admittedParticipants answers true for everybody when the trust list is empty, and admits an unthrottled participant whether or not it names one.
func admittedParticipants(trust, unthrottled []string) func(participant string) bool {
	if len(trust) == 0 {
		return func(string) bool { return true }
	}
	admitted := namedParticipants(trust)
	for _, participant := range unthrottled {
		admitted[strings.TrimSpace(participant)] = true
	}
	return func(participant string) bool { return admitted[participant] }
}

// waivesThrottling stays nil when no list waives anything, so an unset list adds no rung of its own.
func waivesThrottling(unthrottled []string) func(participant string) bool {
	if len(unthrottled) == 0 {
		return nil
	}
	named := namedParticipants(unthrottled)
	return func(participant string) bool { return named[participant] }
}

func namedParticipants(list []string) map[string]bool {
	named := make(map[string]bool, len(list))
	for _, participant := range list {
		named[strings.TrimSpace(participant)] = true
	}
	return named
}

// reachableByTrust is built once per pick, and an empty trust list with no shadow list skips the walk entirely.
func reachableByTrust(participants config.Participants) func(Escrow) bool {
	if len(participants.Trust) == 0 && len(participants.Shadow) == 0 {
		return func(Escrow) bool { return true }
	}
	admitted := admittedParticipants(participants.Trust, participants.Unthrottled)
	shadow := namedParticipants(participants.Shadow)
	routable := func(participant string) bool { return admitted(participant) && !shadow[participant] }
	return func(candidate Escrow) bool {
		return candidate.Session != nil &&
			slices.ContainsFunc(candidate.Session.ParticipantKeys(), routable)
	}
}

func shadowedOn(settings *config.Config, model string) func(participant string) bool {
	if len(settings.For(model).Scheduler.Participants.Shadow) == 0 {
		return nil
	}
	return func(participant string) bool { return settings.ShadowFor(participant, model) }
}

func (s *Scheduler) settingsInForce() *config.Config {
	if s.settings == nil {
		return &config.Config{}
	}
	return s.settings.Load()
}
