package timeoutclaim

import (
	"bytes"
	"reflect"
	"testing"
)

type recordingFinishSource struct {
	finishTx []byte
	asked    []uint64
}

func (s *recordingFinishSource) FinishTxFor(inferenceID uint64) []byte {
	s.asked = append(s.asked, inferenceID)
	return s.finishTx
}

// Test flow:
//  1. Define a table of Finish transactions: non-empty, a single byte, nil and empty non-nil.
//  2. For each case, call `ClaimsMiss` with the Finish.
//  3. Assert it holds exactly for the non-empty Finish transactions.
func TestClaimsMissHoldsExactlyForANonEmptyFinish(t *testing.T) {
	cases := []struct {
		name     string
		finishTx []byte
		want     bool
	}{
		{name: "a Finish claims", finishTx: []byte("finish-tx"), want: true},
		{name: "a one-byte Finish claims", finishTx: []byte{0}, want: true},
		{name: "a nil Finish votes", finishTx: nil, want: false},
		{name: "an empty Finish votes", finishTx: []byte{}, want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ClaimsMiss(testCase.finishTx); got != testCase.want {
				t.Errorf("ClaimsMiss(%q) = %v, want %v", testCase.finishTx, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Define a table of the reply's Finish (non-empty, nil, empty non-nil) and the session's queued Finish (another non-empty one, none, or no session at all).
//  2. For each case, call `FinishTx` with the reply's Finish, a nonce and the session.
//  3. Assert it returns the reply's Finish whenever the reply carried one, without asking the session, and otherwise the session's Finish for that nonce, or nil when there is no session.
func TestFinishTxTakesTheReplysFinishBeforeTheSessions(t *testing.T) {
	const nonce = uint64(42)
	replyFinishTx := []byte("reply-finish-tx")
	queuedFinishTx := []byte("queued-finish-tx")
	cases := []struct {
		name          string
		replyFinishTx []byte
		session       *recordingFinishSource
		want          []byte
		wantAsked     []uint64
	}{
		{name: "the reply's Finish wins over a different queued one", replyFinishTx: replyFinishTx, session: &recordingFinishSource{finishTx: queuedFinishTx}, want: replyFinishTx},
		{name: "the reply's Finish is taken when the session queued none", replyFinishTx: replyFinishTx, session: &recordingFinishSource{}, want: replyFinishTx},
		{name: "a reply without a Finish takes the queued one", session: &recordingFinishSource{finishTx: queuedFinishTx}, want: queuedFinishTx, wantAsked: []uint64{nonce}},
		{name: "an empty reply Finish takes the queued one", replyFinishTx: []byte{}, session: &recordingFinishSource{finishTx: queuedFinishTx}, want: queuedFinishTx, wantAsked: []uint64{nonce}},
		{name: "neither carries one", session: &recordingFinishSource{}, wantAsked: []uint64{nonce}},
		{name: "no session takes the reply's Finish", replyFinishTx: replyFinishTx, want: replyFinishTx},
		{name: "no session and no reply Finish", want: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var queued FinishSource
			if testCase.session != nil {
				queued = testCase.session
			}

			got := FinishTx(testCase.replyFinishTx, nonce, queued)

			if !bytes.Equal(got, testCase.want) {
				t.Errorf("FinishTx = %q, want %q", got, testCase.want)
			}
			if testCase.session != nil && !reflect.DeepEqual(testCase.session.asked, testCase.wantAsked) {
				t.Errorf("session asked for %v, want %v", testCase.session.asked, testCase.wantAsked)
			}
		})
	}
}
