package journal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/internal/logcapture"
	"devshard/host"
)

// Test flow:
//  1. Report signed, unsigned, invalid and absent frame observations through a journal.
//  2. Flush the journal and inspect its rendered entries.
//  3. Assert failures warn, unsigned streams do not warn, and every rendered observation carries the frame counts.
func TestChatFrameChecksAreObservationsOnly(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		check          host.ChatFrameCheck
		level          string
		hasObservation bool
	}{
		{name: "signed", check: host.ChatFrameCheck{Frames: 9, Signed: 8}, level: "info", hasObservation: true},
		{name: "unsigned", check: host.ChatFrameCheck{Frames: 9}, level: "info", hasObservation: true},
		{name: "invalid", check: host.ChatFrameCheck{Frames: 9, Invalid: "invalid signature"}, level: "warn", hasObservation: true},
		{name: "absent"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			lines := &logcapture.Recorder{}
			events := newJournal(t, Settings{Lines: lines})
			events.ChatFramesChecked("7", hostAlpha, 3, testCase.check)
			events.Flush()
			entries := lines.All()
			if !testCase.hasObservation {
				require.Empty(t, entries)
				return
			}
			require.Len(t, entries, 1)
			require.Equal(t, testCase.level, entries[0].Level)
			count, present := loggedValue(entries[0].Fields, "chat_frames")
			require.True(t, present)
			require.Equal(t, testCase.check.Frames, count)
			count, present = loggedValue(entries[0].Fields, "chat_frames_signed")
			require.True(t, present)
			require.Equal(t, testCase.check.Signed, count)
			if testCase.check.Invalid != noChatFrameFailure {
				require.Equal(t, "chat_frame_signature_invalid", entries[0].Msg)
				failure, present := loggedValue(entries[0].Fields, "error")
				require.True(t, present)
				require.Equal(t, testCase.check.Invalid, failure)
			}
		})
	}
	require.False(t, KindChatFramesChecked.onMoneyLane())
}
