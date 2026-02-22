package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// handleSSE handles GET /environments/{id}/events.
//
// On connect it replays all events from seq 0 (or Last-Event-ID for
// reconnection), then streams new events as they arrive. The stream stays
// open until the client disconnects or the server shuts down.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.getInstance(w, r)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Support reconnection: resume from the last event the client saw.
	var fromSeq uint64
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if seq, err := strconv.ParseUint(lastID, 10, 64); err == nil {
			fromSeq = seq
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Only stream lifecycle events over SSE — service.log is high-volume
	// and not needed for coordination. Logs are still captured in the event
	// log and available via GET /log and the timeline on DELETE.
	filter := func(e Event) bool {
		return e.Type != EventServiceLog
	}
	ch := inst.log.Subscribe(r.Context(), fromSeq, filter)
	for event := range ch {
		if err := writeSSEEvent(w, flusher, event); err != nil {
			return // client disconnected
		}
	}
}

// writeSSEEvent formats and flushes a single SSE frame.
//
// Format:
//
//	id: <seq>
//	event: <type>
//	data: <json>
//	(blank line)
//
// The id field maps directly to Last-Event-ID, enabling reconnection without
// replaying events the client has already seen.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n",
		event.Seq, event.Type, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
