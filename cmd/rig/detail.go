package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func renderDetail(w io.Writer, rows []rigdata.TrafficRow, index int) error {
	// Build service color map from all rows for consistency with table view.
	serviceIndex := map[string]int{}
	for _, r := range rows {
		for _, name := range []string{r.Source, r.Target} {
			if _, ok := serviceIndex[name]; !ok {
				serviceIndex[name] = len(serviceIndex)
			}
		}
	}
	serviceColorTotal = len(serviceIndex)

	var target *rigdata.TrafficRow
	for i := range rows {
		if rows[i].Index == index {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("request #%d not found (have %d–%d)", index, rows[0].Index, rows[len(rows)-1].Index)
	}

	r := target
	src := colorService(r.Source, serviceIndex[r.Source])
	tgt := colorService(r.Target, serviceIndex[r.Target])
	fmt.Fprintf(w, "%s\n", bold(fmt.Sprintf("── Request #%d ─────────────────────────────────────────────────────", r.Index)))
	fmt.Fprintf(w, "  %s  %s → %s  %s  %s  %s  %s\n", dim(r.Time), src, tgt, colorMethod(r.Protocol), r.Path, colorStatus(r.Status), dim(r.Latency))

	switch r.Event.Type {
	case rigdata.TypeRequestCompleted:
		renderHTTPDetail(w, r.Event.Request)
	case rigdata.TypeGRPCCallCompleted:
		renderGRPCDetail(w, r.Event.GRPCCall)
	case rigdata.TypeConnectionClosed:
		renderTCPDetail(w, r.Event.Connection)
	case rigdata.TypeKafkaRequestCompleted:
		renderKafkaDetail(w, r.Event.KafkaRequest)
	}
	return nil
}

func renderHTTPDetail(w io.Writer, r *rigdata.RequestInfo) {
	if len(r.RequestHeaders) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Request Headers:"))
		writeHeaders(w, r.RequestHeaders)
	}
	if len(r.RequestBody) > 0 {
		label := fmt.Sprintf("Request Body (%s)", rigdata.FormatBytes(int64(len(r.RequestBody))))
		if r.RequestBodyTruncated {
			label += " [truncated]"
		}
		fmt.Fprintf(w, "\n  %s\n", bold(label+":"))
		writeBody(w, r.RequestBody, headerValue(r.RequestHeaders, "Content-Type"))
	}
	if len(r.ResponseHeaders) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Response Headers:"))
		writeHeaders(w, r.ResponseHeaders)
	}
	if len(r.ResponseBody) > 0 {
		label := fmt.Sprintf("Response Body (%s)", rigdata.FormatBytes(int64(len(r.ResponseBody))))
		if r.ResponseBodyTruncated {
			label += " [truncated]"
		}
		fmt.Fprintf(w, "\n  %s\n", bold(label+":"))
		writeBody(w, r.ResponseBody, headerValue(r.ResponseHeaders, "Content-Type"))
	}
}

func renderGRPCDetail(w io.Writer, g *rigdata.GRPCCallInfo) {
	if g.GRPCMessage != "" {
		fmt.Fprintf(w, "\n  %s %s\n", bold("gRPC Message:"), g.GRPCMessage)
	}
	if len(g.RequestMetadata) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Request Metadata:"))
		writeHeaders(w, g.RequestMetadata)
	}
	// Prefer decoded bodies when available.
	if len(g.RequestBodyDecoded) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Request Body (decoded):"))
		writeBody(w, g.RequestBodyDecoded, "application/json")
	} else if len(g.RequestBody) > 0 {
		label := fmt.Sprintf("Request Body (%s)", rigdata.FormatBytes(int64(len(g.RequestBody))))
		if g.RequestBodyTruncated {
			label += " [truncated]"
		}
		fmt.Fprintf(w, "\n  %s\n", bold(label+":"))
		writeHex(w, g.RequestBody)
	}
	if len(g.ResponseMetadata) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Response Metadata:"))
		writeHeaders(w, g.ResponseMetadata)
	}
	if len(g.ResponseBodyDecoded) > 0 {
		fmt.Fprintf(w, "\n  %s\n", bold("Response Body (decoded):"))
		writeBody(w, g.ResponseBodyDecoded, "application/json")
	} else if len(g.ResponseBody) > 0 {
		label := fmt.Sprintf("Response Body (%s)", rigdata.FormatBytes(int64(len(g.ResponseBody))))
		if g.ResponseBodyTruncated {
			label += " [truncated]"
		}
		fmt.Fprintf(w, "\n  %s\n", bold(label+":"))
		writeHex(w, g.ResponseBody)
	}
}

func renderKafkaDetail(w io.Writer, k *rigdata.KafkaRequestInfo) {
	fmt.Fprintf(w, "\n  %s        %s (key %d)\n", bold("API Name:"), k.APIName, k.APIKey)
	fmt.Fprintf(w, "  %s     %d\n", bold("API Version:"), k.APIVersion)
	fmt.Fprintf(w, "  %s  %d\n", bold("Correlation ID:"), k.CorrelationID)
	fmt.Fprintf(w, "  %s    %s\n", bold("Request Size:"), rigdata.FormatBytes(k.RequestSize))
	fmt.Fprintf(w, "  %s   %s\n", bold("Response Size:"), rigdata.FormatBytes(k.ResponseSize))
	fmt.Fprintf(w, "  %s         %s\n", bold("Latency:"), rigdata.FormatLatency(k.LatencyMs))
}

func renderTCPDetail(w io.Writer, c *rigdata.ConnectionInfo) {
	fmt.Fprintf(w, "\n  %s   %s\n", bold("Bytes In:"), rigdata.FormatBytes(c.BytesIn))
	fmt.Fprintf(w, "  %s  %s\n", bold("Bytes Out:"), rigdata.FormatBytes(c.BytesOut))
	fmt.Fprintf(w, "  %s   %s\n", bold("Duration:"), rigdata.FormatLatency(c.DurationMs))
}

func writeHeaders(w io.Writer, headers map[string][]string) {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range headers[k] {
			fmt.Fprintf(w, "    %s: %s\n", k, v)
		}
	}
}

func writeBody(w io.Writer, body []byte, contentType string) {
	if isJSON(contentType) || json.Valid(body) {
		var buf bytes.Buffer
		if json.Indent(&buf, body, "", "  ") == nil {
			for _, line := range strings.Split(buf.String(), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			return
		}
	}
	// Plain text fallback.
	for _, line := range strings.Split(string(body), "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}

func writeHex(w io.Writer, data []byte) {
	// Simple hex dump, 16 bytes per line.
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		hex := make([]string, end-i)
		for j, b := range data[i:end] {
			hex[j] = fmt.Sprintf("%02x", b)
		}
		fmt.Fprintf(w, "    %s\n", strings.Join(hex, " "))
	}
}

func headerValue(headers map[string][]string, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func isJSON(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "json")
}
