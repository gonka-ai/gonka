package config

import (
	"slices"
	"strings"
)

func applyParticipants(target *Participants, source ParticipantOverrides) {
	for _, list := range []struct {
		replacement *[]string
		destination *[]string
	}{
		{source.Trust, &target.Trust},
		{source.Shadow, &target.Shadow},
		{source.Unthrottled, &target.Unthrottled},
	} {
		if list.replacement != nil {
			*list.destination = slices.Clone(*list.replacement)
		}
	}
}

const (
	listTrust       = "trust"
	listShadow      = "shadow"
	listUnthrottled = "unthrottled"
)

type namedList struct {
	name    string
	entries []string
}

// validate refuses an entry that is no address, and a participant named both to be probed only and to be routed client traffic.
func (p Participants) validate(complain func(format string, args ...any)) {
	routed := []namedList{{listTrust, p.Trust}, {listUnthrottled, p.Unthrottled}}
	for _, list := range append([]namedList{{listShadow, p.Shadow}}, routed...) {
		for index, participant := range list.entries {
			if problem := participantEntryProblem(participant); problem != "" {
				complain("participants.%s[%d]: %s", list.name, index, problem)
			}
		}
	}
	for _, shadowed := range p.Shadow {
		for _, list := range routed {
			if namesParticipant(list.entries, shadowed) {
				complain("participants.%s: %s is also in participants.%s, which routes client traffic to it", listShadow, strings.TrimSpace(shadowed), list.name)
			}
		}
	}
}

func namesParticipant(entries []string, participant string) bool {
	return slices.ContainsFunc(entries, func(entry string) bool {
		return strings.TrimSpace(entry) == strings.TrimSpace(participant)
	})
}
