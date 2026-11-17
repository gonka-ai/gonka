package engine

import (
	"io"
	"net/http"
)

// streamVerdict is the coordinator's answer to one attempt's claim on the client stream.
type streamVerdict int

const (
	streamPending streamVerdict = iota
	streamWinner
	streamSuppressed
)

// crownRequest is an attempt's claim on the client stream, answered on Reply exactly once. See race.md, "Crowning".
type crownRequest struct {
	Nonce uint64
	Reply chan<- streamVerdict
}

// winnerWriter is one attempt's claim on the client stream; a losing attempt has no reachable sink at all. See README, "Crowning and the client's bytes".
type winnerWriter struct {
	nonce    uint64
	crown    chan<- crownRequest
	reply    chan streamVerdict
	raceDone <-chan struct{}

	client   io.Writer
	withheld io.Writer

	verdict         streamVerdict
	prefix          []byte
	prefixAllowance prefixAllowance
}

func newWinnerWriter(nonce uint64, client io.Writer, crown chan<- crownRequest, raceDone <-chan struct{}, allowance prefixAllowance) *winnerWriter {
	return &winnerWriter{
		nonce:           nonce,
		crown:           crown,
		reply:           make(chan streamVerdict, 1),
		raceDone:        raceDone,
		withheld:        client,
		prefixAllowance: allowance,
	}
}

// Write claims the crown on the first content chunk and buffers everything before it. See race.md, "Crowning".
func (w *winnerWriter) Write(chunk []byte, hasContent bool) error {
	switch w.verdict {
	case streamWinner:
		return w.forward(chunk)
	case streamSuppressed:
		return nil
	}
	if !hasContent {
		w.buffer(chunk)
		return nil
	}
	if w.claim() == streamWinner {
		return w.forward(chunk)
	}
	return nil
}

// Flush needs no gate of its own: an uncrowned attempt has no client to reach.
func (w *winnerWriter) Flush() {
	if flusher, ok := w.client.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Abandon forfeits this attempt's claim: what an uncrowned attempt buffered must never be forwarded.
func (w *winnerWriter) Abandon() {
	w.verdict, w.prefix, w.client, w.withheld = streamSuppressed, nil, nil, nil
	w.prefixAllowance.release()
}

func (w *winnerWriter) releasePrefix() {
	w.prefix = nil
	w.prefixAllowance.release()
}

func (w *winnerWriter) claim() streamVerdict {
	select {
	case w.crown <- crownRequest{Nonce: w.nonce, Reply: w.reply}:
	case <-w.raceDone:
		w.Abandon()
		return w.verdict
	}
	select {
	case verdict := <-w.reply:
		if verdict == streamWinner {
			w.verdict, w.client, w.withheld = streamWinner, w.withheld, nil
			return w.verdict
		}
		w.Abandon()
	case <-w.raceDone:
		w.Abandon()
	}
	return w.verdict
}

func (w *winnerWriter) forward(chunk []byte) error {
	prefix := w.prefix
	w.prefix = nil
	w.prefixAllowance.release()
	if w.client == nil {
		return nil
	}
	if len(prefix) > 0 {
		if _, err := w.client.Write(prefix); err != nil {
			return err
		}
	}
	_, err := w.client.Write(chunk)
	return err
}

// buffer holds pre-content bytes until a claim can flush them; past the prefix budget it ends the attempt. See race.md, "Crowning".
func (w *winnerWriter) buffer(chunk []byte) {
	select {
	case <-w.raceDone:
		w.Abandon()
		return
	default:
	}
	if err := w.prefixAllowance.charge(int64(len(chunk))); err != nil {
		w.Abandon()
		w.prefixAllowance.overflow(err)
		return
	}
	w.prefix = append(w.prefix, chunk...)
}
