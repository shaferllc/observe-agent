// observe-agent reports a Linux server's CPU, memory, swap, disk, load, network, uptime and watched processes to
// Observe (observe.live) once a minute, buffering while Observe is unreachable.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "dev" // set by GoReleaser

func main() {
	once := flag.Bool("once", false, "print one sample as JSON and exit (to check what the agent reads)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	collector := NewCollector()
	if *once {
		collector.Collect(time.Now(), nil)
		time.Sleep(time.Second)
		out, _ := json.MarshalIndent(collector.Collect(time.Now(), strings.Fields(os.Getenv("OBSERVE_PROCESSES"))), "", "  ")
		fmt.Println(string(out))
		return
	}

	token := strings.TrimSpace(os.Getenv("OBSERVE_TOKEN"))
	if token == "" {
		log.Fatal("OBSERVE_TOKEN is not set (the server's heartbeat token from its page in Observe)")
	}
	url := strings.TrimSpace(os.Getenv("OBSERVE_URL"))
	if url == "" {
		url = "https://observe.live"
	}

	hostname, _ := os.Hostname()
	client := &Client{
		URL:      url,
		Token:    token,
		Version:  version,
		Info:     map[string]string{"version": version, "hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH, "kernel": kernelRelease()},
		HTTP:     &http.Client{Timeout: 20 * time.Second},
		Interval: time.Minute,
	}
	buffer := &Buffer{Max: 1440}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("observe-agent %s reporting to %s", version, url)
	collector.Collect(time.Now(), nil) // prime the CPU and network counters
	timer := time.NewTimer(client.Interval)
	for {
		select {
		case <-stop:
			_ = client.Flush(buffer, time.Now())
			return
		case now := <-timer.C:
			buffer.Push(collector.Collect(now, client.Processes))
			if err := client.Flush(buffer, now); err != nil {
				log.Printf("%v (%d sample(s) buffered)", err, buffer.Len())
			}
			timer.Reset(client.Interval)
		}
	}
}

func kernelRelease() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
