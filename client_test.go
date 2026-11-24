package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBufferDropsOldestPastMax(t *testing.T) {
	b := &Buffer{Max: 3}
	for i := int64(1); i <= 5; i++ {
		b.Push(Sample{TS: i})
	}
	if b.Len() != 3 || b.samples[0].TS != 3 {
		t.Fatalf("buffer = %+v", b.samples)
	}
}

func TestFlushSendsInBatchesWithTokenAndLearnsProcesses(t *testing.T) {
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/metrics" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("bad request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct{ Samples []Sample }
		json.NewDecoder(r.Body).Decode(&body)
		batches = append(batches, len(body.Samples))
		w.Write([]byte(`{"ok":true,"interval":30,"processes":["nginx"]}`))
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, Token: "tok", HTTP: srv.Client(), Interval: time.Minute}
	b := &Buffer{Max: 1000}
	for i := 0; i < BatchSize+5; i++ {
		b.Push(Sample{TS: int64(i)})
	}
	if err := c.Flush(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0] != BatchSize || batches[1] != 5 || b.Len() != 0 {
		t.Fatalf("batches = %v, left %d", batches, b.Len())
	}
	if c.Interval != 30*time.Second || len(c.Processes) != 1 || c.Processes[0] != "nginx" {
		t.Fatalf("client = %+v", c)
	}
}

func TestFlushKeepsSamplesWhenOfflineOrThrottled(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	c := &Client{URL: srv.URL, Token: "tok", HTTP: srv.Client()}
	b := &Buffer{Max: 10}
	b.Push(Sample{TS: 1})
	now := time.Now()
	if err := c.Flush(b, now); err == nil || b.Len() != 1 {
		t.Fatalf("429 must keep the sample: err=%v len=%d", err, b.Len())
	}
	c.Flush(b, now.Add(time.Minute))
	if calls != 1 {
		t.Fatalf("must wait out Retry-After, called %d times", calls)
	}
	srv.Close()
	if err := c.Flush(b, now.Add(3*time.Minute)); err == nil || b.Len() != 1 {
		t.Fatal("unreachable Observe must keep the sample")
	}
}

func TestFlushDropsARejectedBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "tok", HTTP: srv.Client()}
	b := &Buffer{Max: 10}
	b.Push(Sample{TS: 1})
	if err := c.Flush(b, time.Now()); err == nil || b.Len() != 0 {
		t.Fatalf("422 should drop the batch: err=%v len=%d", err, b.Len())
	}
}
