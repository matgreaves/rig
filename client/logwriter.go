package rig

import (
	"bytes"
	"strings"
	"sync"
)

// rigLogWriter is an io.Writer that ships log lines to rigd as service.log
// events. Partial writes are buffered until a newline is seen. Complete lines
// are sent to a background goroutine via a channel so that Write never blocks
// on HTTP I/O.
//
// Safe for concurrent use.
type rigLogWriter struct {
	serverURL string
	envID     string
	service   string

	mu   sync.Mutex
	buf  bytes.Buffer
	ch   chan string
	done chan struct{}
}

func newLogWriter(serverURL, envID, service string) *rigLogWriter {
	w := &rigLogWriter{
		serverURL: serverURL,
		envID:     envID,
		service:   service,
		ch:        make(chan string, 256),
		done:      make(chan struct{}),
	}
	go w.drain()
	return w
}

// drain batches queued log lines and posts them to rigd. Each iteration
// takes one line from the channel, then drains any additional lines that
// are ready, and sends them as a single newline-joined event. This
// naturally batches bursts while keeping latency low during quiet periods.
func (w *rigLogWriter) drain() {
	defer close(w.done)
	for first := range w.ch {
		var batch []string
		batch = append(batch, first)

		// Drain any additional lines that are already queued.
	gather:
		for {
			select {
			case line, ok := <-w.ch:
				if !ok {
					break gather
				}
				batch = append(batch, line)
			default:
				break gather
			}
		}

		postClientEvent(w.serverURL, w.envID, struct {
			Type    string `json:"type"`
			Service string `json:"service"`
			Stream  string `json:"stream"`
			LogData string `json:"log_data"`
		}{
			Type:    "service.log",
			Service: w.service,
			Stream:  "stdout",
			LogData: strings.Join(batch, "\n"),
		})
	}
}

// Write implements io.Writer. Buffers partial lines and enqueues complete
// lines for async delivery to rigd.
func (w *rigLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)

	// Flush all complete lines.
	for {
		line, err := w.buf.ReadBytes('\n')
		if err != nil {
			// No newline found — put the partial line back.
			w.buf.Write(line)
			break
		}
		w.enqueue(string(bytes.TrimRight(line, "\n")))
	}

	return len(p), nil
}

// Flush sends any remaining buffered data and waits for the background
// goroutine to finish delivering all queued lines.
func (w *rigLogWriter) Flush() {
	w.mu.Lock()
	if w.buf.Len() > 0 {
		w.enqueue(w.buf.String())
		w.buf.Reset()
	}
	w.mu.Unlock()

	close(w.ch)
	<-w.done // wait for all lines to be posted
}

// enqueue sends a line to the background goroutine. Drops the line if the
// channel is full (writer should never block the caller).
func (w *rigLogWriter) enqueue(line string) {
	if line == "" {
		return
	}
	select {
	case w.ch <- line:
	default:
		// channel full — drop line rather than block
	}
}
