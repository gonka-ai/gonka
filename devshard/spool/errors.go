package spool

import "errors"

var (
	// ErrNoCapacity is returned when MaxFiles is reached.
	ErrNoCapacity = errors.New("spool: no capacity")

	// ErrBudgetExceeded is returned when a RAM or disk charge would exceed the budget.
	ErrBudgetExceeded = errors.New("spool: budget exceeded")

	// ErrFileTooLarge is returned when a write would exceed MaxFileBytes.
	ErrFileTooLarge = errors.New("spool: file too large")

	// ErrClosed is returned when an operation runs after Close.
	ErrClosed = errors.New("spool: closed")

	// ErrIndexPast is returned when an index record number is out of range.
	ErrIndexPast = errors.New("spool: index past tip")

	// ErrDisabled is returned when an operation needs a file but Dir is disabled.
	ErrDisabled = errors.New("spool: disabled")

	// ErrUnlimitedRejected is returned by Open/Reconfigure when a zero cap is
	// set without AllowUnlimited.
	ErrUnlimitedRejected = errors.New("spool: unlimited requires AllowUnlimited")
)
