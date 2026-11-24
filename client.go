package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BatchSize matches the most Observe takes in one request.
const BatchSize = 200

// Buffer holds samples not yet sent, oldest first, dropping the oldest past Max (a day at one a minute).
type Buffer struct {
	Max     int
	samples []Sample
}

func (b *Buffer) Push(s Sample) {
	b.samples = append(b.samples, s)
	if over := len(b.samples) - b.Max; over > 0 {
		b.samples = append([]Sample(nil), b.samples[over:]...)
	}
}

func (b *Buffer) Len() int { return len(b.samples) }

// Client posts batches to Observe.
type Client struct {
	URL       string // https://observe.live
	Token     string
	Version   string
	Info      map[string]string
	HTTP      *http.Client
	Processes []string      // names Observe asked us to watch (sent back with every reply)
	Interval  time.Duration // how often Observe wants a sample
	notBefore time.Time     // after a 429 or 503, wait until then
}

type reply struct {
	Interval  int      `json:"interval"`
	Processes []string `json:"processes"`
}

// errRetry means keep the batch and try again later; any other error means the batch was refused for good.
type errRetry struct{ msg string }

func (e errRetry) Error() string { return e.msg }

// Flush sends the buffer oldest first, a batch at a time, until it is empty or a send fails. A batch Observe
// rejects as invalid is dropped (resending it would fail forever); anything else stays for the next try.
func (c *Client) Flush(b *Buffer, now time.Time) error {
	if now.Before(c.notBefore) {
		return nil
	}
	for b.Len() > 0 {
		n := min(BatchSize, b.Len())
		err := c.send(b.samples[:n], now)
		if _, retry := err.(errRetry); retry {
			return err
		}
		b.samples = b.samples[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) send(samples []Sample, now time.Time) error {
	body, err := json.Marshal(map[string]any{"agent": c.Info, "samples": samples})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.URL, "/")+"/api/agent/v1/metrics", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "observe-agent/"+c.Version)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errRetry{err.Error()}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var r reply
		if json.NewDecoder(resp.Body).Decode(&r) == nil {
			c.Processes = r.Processes
			if r.Interval >= 10 {
				c.Interval = time.Duration(r.Interval) * time.Second
			}
		}
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		wait := 60
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = s
		}
		c.notBefore = now.Add(time.Duration(wait) * time.Second)
		return errRetry{fmt.Sprintf("observe answered %d; retrying in %ds", resp.StatusCode, wait)}
	case resp.StatusCode == http.StatusUnauthorized:
		// Keep the samples: the heartbeat may be turned back on, or the token fixed, without losing the gap.
		return errRetry{"observe refused the token (401): check OBSERVE_TOKEN and that the server's heartbeat is on"}
	default:
		return fmt.Errorf("observe rejected a batch (%d); dropped it", resp.StatusCode)
	}
}
