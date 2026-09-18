package worker_test

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"trainshard/internal/application/hostd/run/worker"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type recording struct{ waits []string }

func (r *recording) Enabled(context.Context, slog.Level) bool { return true }

func (r *recording) Handle(_ context.Context, record slog.Record) error {
	wait := ""
	record.Attrs(func(a slog.Attr) bool {
		if a.Key == "waiting_for" {
			wait = a.Value.String()
		}
		return true
	})
	r.waits = append(r.waits, wait)
	return nil
}

func (r *recording) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *recording) WithGroup(string) slog.Handler { return r }

func TestNoticesSpeakOncePerChange(t *testing.T) {
	// arrange
	rec := &recording{}
	notices := worker.NewNotices(slog.New(rec))
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node1"}
	reserved := func(waiting string) run.Outcome { return run.Outcome{Reserved: true, Waiting: waiting} }

	// act
	notices.Note(node, reserved("one thing"))
	notices.Note(node, reserved("one thing"))
	notices.Note(node, reserved("another"))
	notices.Note(node, reserved(""))
	notices.Note(node, reserved(""))

	// assert
	want := []string{"one thing", "another", ""}
	if !reflect.DeepEqual(rec.waits, want) {
		t.Fatalf("logged %q, want one record per change %q", rec.waits, want)
	}
}

func TestNoticesAreSilentAboutANodeNobodyReserved(t *testing.T) {
	// arrange
	rec := &recording{}
	notices := worker.NewNotices(slog.New(rec))
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node1"}

	// act
	notices.Note(node, run.Outcome{Reserved: true, Waiting: "one thing"})
	notices.Note(node, run.Outcome{})
	notices.Note(node, run.Outcome{})

	// assert
	if want := []string{"one thing"}; !reflect.DeepEqual(rec.waits, want) {
		t.Fatalf("logged %q, want an idle node to pass without a word %q", rec.waits, want)
	}
}
