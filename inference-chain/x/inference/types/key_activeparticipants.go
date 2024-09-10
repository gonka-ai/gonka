package types

const activeParticipantsKey = "ActiveParticipants/value/"

func ActiveParticipantsKey() []byte {
	return []byte(activeParticipantsKey)
}
