package metrics

import "testing"

// Test flow:
//  1. Register a `JournalCollector` backed by a source function that returns fixed `JournalCounts`.
//  2. Assert the money-refused, progress-dropped, and late-events counters report those values.
func TestTheJournalCollectorReportsWhatTheJournalCounted(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewJournalCollector(func() JournalCounts {
		return JournalCounts{MoneyRefused: 3, ProgressDropped: 5, LateEvents: 1}
	}))

	expectCounter(t, telemetry, "devshard_gateway_journal_money_refused_total", labels{}, 3)
	expectCounter(t, telemetry, "devshard_gateway_journal_progress_dropped_total", labels{}, 5)
	expectCounter(t, telemetry, "devshard_gateway_journal_late_events_total", labels{}, 1)
}

// Test flow:
//  1. Register a `JournalCollector` with a nil source function.
//  2. Assert the money-refused-total series count is zero.
func TestAJournalCollectorWithNoSourceReportsNothing(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewJournalCollector(nil))

	expectSeriesCount(t, telemetry, "devshard_gateway_journal_money_refused_total", 0)
}
