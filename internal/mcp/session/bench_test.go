package session

import (
	"context"
	"encoding/json"
	"testing"
)

var benchBeat = json.RawMessage(`{"time":"2026-11-12T10:00:00Z","event":"heartbeat","stage":1,"total_stages":1,"target_rps":120,"actual_rps":118.2,"total_requests":4210,"success_count":4208,"failure_count":2,"error_rate":0.0005,"p50_ms":9.3,"p95_ms":9.7,"p99_ms":10.3,"bias":0,"elapsed_s":42,"hold_rps":118,"window":{"seconds":5,"p99_ms":10.3,"error_rate":0,"samples":590},"autopilot":{"mode":"find-ceiling","state":"confirming"}}`)

// One heartbeat reaching one plugin subscriber: queue it, read it back, render the SSE data.
func BenchmarkSubscription_PushNextJSON(b *testing.B) {
	sub := newSubscription(nil)
	ctx := context.Background()
	ev := Event{Name: "heartbeat", Envelope: &Envelope{SessionID: "sim_bench", Origin: OriginAgent, Autopilot: "find-ceiling", LCP: benchBeat}}
	b.ReportAllocs()
	b.SetBytes(int64(len(benchBeat)))
	for i := 0; i < b.N; i++ {
		sub.push(ev)
		got, err := sub.Next(ctx)
		if err != nil {
			b.Fatal(err)
		}
		_ = got.JSON()
	}
}

// A slow reader: the queue stays full, so every push has to drop the oldest heartbeat.
func BenchmarkSubscription_PushWhenFull(b *testing.B) {
	sub := newSubscription(nil)
	ev := Event{Name: "heartbeat", Envelope: &Envelope{SessionID: "sim_bench", Origin: OriginAgent, LCP: benchBeat}}
	for range subBuffer {
		sub.push(ev)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sub.push(ev)
	}
}
