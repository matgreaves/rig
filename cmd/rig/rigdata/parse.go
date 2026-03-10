package rigdata

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseTrafficEvents reads JSONL and returns only traffic-related events.
func ParseTrafficEvents(r io.Reader) ([]Event, error) {
	var events []Event
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // up to 1MB lines
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		switch ev.Type {
		case TypeRequestCompleted, TypeConnectionClosed, TypeGRPCCallCompleted, TypeKafkaRequestCompleted:
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

// BuildRows converts events to display rows, assigning 1-based indices.
func BuildRows(events []Event) []TrafficRow {
	if len(events) == 0 {
		return nil
	}
	t0 := events[0].Timestamp
	rows := make([]TrafficRow, len(events))
	for i, ev := range events {
		rel := ev.Timestamp.Sub(t0)
		row := TrafficRow{
			Index: i + 1,
			Time:  FormatDuration(rel),
			Event: ev,
		}
		switch ev.Type {
		case TypeRequestCompleted:
			r := ev.Request
			row.Source = r.Source
			row.Target = r.Target
			row.Protocol = "HTTP"
			row.Method = r.Method
			row.Path = r.Path
			row.Status = strconv.Itoa(r.StatusCode)
			row.Latency = FormatLatency(r.LatencyMs)
		case TypeGRPCCallCompleted:
			g := ev.GRPCCall
			row.Source = g.Source
			row.Target = g.Target
			row.Protocol = "gRPC"
			row.Method = "gRPC"
			row.Path = g.Service + "/" + g.Method
			row.Status = g.GRPCStatus
			row.Latency = FormatLatency(g.LatencyMs)
		case TypeConnectionClosed:
			c := ev.Connection
			row.Source = c.Source
			row.Target = c.Target
			row.Protocol = "TCP"
			row.Method = "TCP"
			row.Path = "—"
			row.Status = "—"
			row.Latency = FormatLatency(c.DurationMs)
			row.Extra = fmt.Sprintf("%s↑ %s↓", FormatBytes(c.BytesIn), FormatBytes(c.BytesOut))
		case TypeKafkaRequestCompleted:
			k := ev.KafkaRequest
			row.Source = k.Source
			row.Target = k.Target
			row.Protocol = "Kafka"
			row.Method = k.APIName
			row.Path = fmt.Sprintf("v%d cid:%d", k.APIVersion, k.CorrelationID)
			row.Status = "—"
			row.Latency = FormatLatency(k.LatencyMs)
			row.Extra = fmt.Sprintf("%s↑ %s↓", FormatBytes(k.RequestSize), FormatBytes(k.ResponseSize))
		}
		rows[i] = row
	}
	return rows
}

// ApplyFilter returns only rows matching all filter criteria.
func ApplyFilter(rows []TrafficRow, f TrafficFilter) []TrafficRow {
	if f.Edge == "" && f.SlowMs == 0 && f.Status == "" && f.Protocol == "" {
		return rows
	}
	var out []TrafficRow
	for _, r := range rows {
		if !matchEdge(r, f.Edge) {
			continue
		}
		if !matchSlow(r, f.SlowMs) {
			continue
		}
		if !matchStatus(r, f.Status) {
			continue
		}
		if !matchProtocol(r, f.Protocol) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func matchEdge(r TrafficRow, edge string) bool {
	if edge == "" {
		return true
	}
	edge = strings.ReplaceAll(edge, "->", "→")
	if strings.Contains(edge, "→") {
		parts := strings.SplitN(edge, "→", 2)
		src := strings.TrimSpace(parts[0])
		tgt := strings.TrimSpace(parts[1])
		if src != "" && !strings.EqualFold(r.Source, src) {
			return false
		}
		if tgt != "" && !strings.EqualFold(r.Target, tgt) {
			return false
		}
		return true
	}
	return strings.EqualFold(r.Source, edge) || strings.EqualFold(r.Target, edge)
}

func matchSlow(r TrafficRow, thresholdMs float64) bool {
	if thresholdMs == 0 {
		return true
	}
	var latencyMs float64
	switch r.Event.Type {
	case TypeRequestCompleted:
		latencyMs = r.Event.Request.LatencyMs
	case TypeGRPCCallCompleted:
		latencyMs = r.Event.GRPCCall.LatencyMs
	case TypeConnectionClosed:
		latencyMs = r.Event.Connection.DurationMs
	case TypeKafkaRequestCompleted:
		latencyMs = r.Event.KafkaRequest.LatencyMs
	}
	return latencyMs >= thresholdMs
}

func matchStatus(r TrafficRow, status string) bool {
	if status == "" {
		return true
	}
	if len(status) == 3 && status[1] == 'x' && status[2] == 'x' {
		classDigit := status[0]
		if r.Event.Type == TypeRequestCompleted {
			actual := strconv.Itoa(r.Event.Request.StatusCode)
			return len(actual) == 3 && actual[0] == classDigit
		}
		return false
	}
	return strings.EqualFold(r.Status, status)
}

func matchProtocol(r TrafficRow, protocol string) bool {
	if protocol == "" {
		return true
	}
	return strings.EqualFold(r.Protocol, protocol)
}

// ParseLogEvents reads JSONL and returns only log-related events.
func ParseLogEvents(r io.Reader) ([]LogEvent, error) {
	var events []LogEvent
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev LogEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		switch {
		case ev.Type == TypeServiceLog && ev.Log != nil:
			events = append(events, ev)
		case ev.Type == TypeTestNote && ev.Error != "":
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

// ReadHeader reads only the first line of a JSONL file and parses it as a
// log.header event. Returns an error if the first line is not a log.header.
func ReadHeader(path string) (LsHeader, error) {
	f, err := open(path)
	if err != nil {
		return LsHeader{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return LsHeader{}, fmt.Errorf("empty file")
	}

	var hdr LsHeader
	if err := json.Unmarshal(scanner.Bytes(), &hdr); err != nil {
		return LsHeader{}, err
	}
	if hdr.Type != "log.header" {
		return LsHeader{}, fmt.Errorf("not a log.header")
	}
	return hdr, nil
}
