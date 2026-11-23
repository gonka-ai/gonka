package api

import (
	"context"
	"net/http"
)

func serveLog(w http.ResponseWriter, r *http.Request, requestID string, reader *logReader) (escrowID string, written int64) {
	defer reader.release()
	writeChatHeaders(w.Header(), requestID, reader.log.escrow(), contentTypeEventStream, true)
	w.WriteHeader(http.StatusOK)
	written = pumpLog(r.Context(), w, reader)
	return reader.log.escrow(), written
}

func pumpLog(ctx context.Context, w http.ResponseWriter, reader *logReader) (written int64) {
	controller := http.NewResponseController(w)
	for {
		events, finished, changed := reader.next()
		if len(events) > 0 {
			sent, err := w.Write(events)
			written += int64(sent)
			if err != nil {
				return written
			}
			_ = controller.Flush()
			continue
		}
		if finished {
			if reader.dropped() {
				sent, _ := w.Write(errorEvent(ErrResponseBufferFull))
				written += int64(sent)
				_ = controller.Flush()
			}
			return written
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return written
		}
	}
}

func (s *Server) concludeStream(log *streamLog, requestID, model, escrowID string, unstorable error) {
	entry, refusal := log.conclude(escrowID, unstorable)
	s.offerToCache(log.key, entry, refusal, requestID, model)
	s.streams.remove(log)
}
