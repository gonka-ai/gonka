package store

// Role is what an escrow is for in the rotation, as a row stores it.
type Role string

const (
	RoleUnset   Role = ""
	RoleTemp    Role = "temp"
	RoleRegular Role = "regular"
	RoleReserve Role = "reserve"
)
