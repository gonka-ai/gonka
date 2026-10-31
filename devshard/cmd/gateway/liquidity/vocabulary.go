package liquidity

// Class names what one part of an escrow's money is waiting for; the metrics label carries it, and ClassNone is a record that holds none of it. See README.md, "Classes".
type Class string

const (
	ClassNone      Class = ""
	ClassFree      Class = "free"
	ClassReturning Class = "returning"
	ClassLate      Class = "late"
	ClassStuck     Class = "stuck"
)
