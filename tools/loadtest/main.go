// tools/loadtest exercises the platform the way INSTRUCTIONS.md §38 asks for:
// connection capacity and message throughput measured independently. It
// deliberately models "connections" separately from "channel members" —
// membership is capped by the FREE-tier quota (max_channel_members), so this
// tool simulates high connection counts the way a real client would exceed
// membership limits: multiple devices/sockets per member (INSTRUCTIONS.md
// §21), not one connection per member.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

func main() {
	apiURL := flag.String("api-url", "http://localhost:8081", "base URL of an api instance")
	wsURL := flag.String("ws-url", "ws://localhost:8091", "base URL of the matching gateway instance (ignored if --ws-urls is set)")
	wsURLs := flag.String("ws-urls", "", "comma-separated gateway base URLs to round-robin connections across, e.g. ws://localhost:8091,ws://localhost:8094 — spreads per-instance fan-out load instead of pinning every socket to one gateway")
	region := flag.String("region", "eu", "region to create the test users/channel in")
	tier := flag.String("tier", "FREE", "tier to run against — either provisions a fresh org+app at this tier, or is ignored if --app-credentials is set")
	appCredentials := flag.String("app-credentials", "", "existing app credentials as key:secret (skips auto-provisioning an org+app)")
	members := flag.Int("members", 3, "total channel members including the sender (capped by tier quota)")
	connectionsPerMember := flag.Int("connections-per-member", 20, "simulated devices/sockets per member")
	rate := flag.Float64("rate", 2.0, "messages/sec sent by the sender")
	duration := flag.Duration("duration", 20*time.Second, "test duration")
	reconnectStorm := flag.Bool("reconnect-storm", false, "disconnect and reconnect every socket at the midpoint")
	flag.Parse()

	api := newAPIClient(*apiURL)

	var appKey, appSecret string
	if *appCredentials != "" {
		key, secret, ok := strings.Cut(*appCredentials, ":")
		if !ok {
			log.Fatalf("--app-credentials must be in key:secret form")
		}
		appKey, appSecret = key, secret
	} else {
		var err error
		appKey, appSecret, err = api.provisionApp(*tier)
		must(err, "provision app")
	}

	gatewayURLs := []string{*wsURL}
	if *wsURLs != "" {
		gatewayURLs = gatewayURLs[:0]
		for u := range strings.SplitSeq(*wsURLs, ",") {
			if u = strings.TrimSpace(u); u != "" {
				gatewayURLs = append(gatewayURLs, u)
			}
		}
	}

	fmt.Printf("== chat platform load test ==\napi=%s ws=%v region=%s tier=%s members=%d connections/member=%d rate=%.1f/s duration=%s reconnect_storm=%v\n\n",
		*apiURL, gatewayURLs, *region, *tier, *members, *connectionsPerMember, *rate, *duration, *reconnectStorm)

	appToken, err := api.exchangeAppToken(appKey, appSecret)
	must(err, "exchange app token")

	sender, err := api.createUser("loadtest-sender-"+uuid.NewString()[:8], *region, appToken)
	must(err, "create sender")

	channel, err := api.createChannel(sender.Token, "loadtest-channel")
	must(err, "create channel")
	fmt.Printf("channel created: %s\n", channel.ChannelID)

	memberTokens := []string{sender.Token}
	for i := 1; i < *members; i++ {
		u, err := api.createUser(fmt.Sprintf("loadtest-member-%d-%s", i, uuid.NewString()[:8]), *region, appToken)
		must(err, "create member")
		if err := api.addMember(sender.Token, channel.ChannelID, u.UserID); err != nil {
			log.Fatalf("add member %d: %v (tip: raise the account tier or lower --members; FREE defaults to max_channel_members=3)", i, err)
		}
		memberTokens = append(memberTokens, u.Token)
	}
	fmt.Printf("channel membership ready: %d member(s)\n", len(memberTokens))

	latencies := newLatencyRecorder()
	var sockets []*socket
	var connectFailures int
	connIdx := 0
	for _, token := range memberTokens {
		for c := 0; c < *connectionsPerMember; c++ {
			url := gatewayURLs[connIdx%len(gatewayURLs)]
			connIdx++
			s, err := dialSocket(url, token, latencies)
			if err != nil {
				connectFailures++
				continue
			}
			sockets = append(sockets, s)
		}
	}
	fmt.Printf("connections established: %d (failed: %d)\n\n", len(sockets), connectFailures)

	stop := make(chan struct{})
	var sent, accepted, rateLimited int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		interval := time.Duration(float64(time.Second) / *rate)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				i++
				sent++
				limited, err := api.sendMessage(sender.Token, channel.ChannelID, uuid.NewString(),
					fmt.Sprintf("load test message #%d", i))
				if err != nil {
					continue
				}
				if limited {
					rateLimited++
				} else {
					accepted++
				}
			}
		}
	}()

	if *reconnectStorm {
		time.AfterFunc(*duration/2, func() {
			fmt.Println("-- reconnect storm: dropping and re-establishing every connection --")
			var reconnectWG sync.WaitGroup
			for i, s := range sockets {
				reconnectWG.Add(1)
				go func(i int, old *socket) {
					defer reconnectWG.Done()
					old.close()
					token := memberTokens[i%len(memberTokens)]
					url := gatewayURLs[i%len(gatewayURLs)]
					time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)
					if ns, err := dialSocket(url, token, latencies); err == nil {
						sockets[i] = ns
					}
				}(i, s)
			}
			reconnectWG.Wait()
			fmt.Println("-- reconnect storm complete --")
		})
	}

	time.Sleep(*duration)
	close(stop)
	wg.Wait()

	for _, s := range sockets {
		s.close()
	}
	time.Sleep(250 * time.Millisecond) // let in-flight deliveries land

	report := latencies.report()
	fmt.Println()
	fmt.Println("== results ==")
	fmt.Printf("messages sent:          %d\n", sent)
	fmt.Printf("messages accepted:      %d\n", accepted)
	fmt.Printf("messages rate-limited:  %d\n", rateLimited)
	fmt.Printf("delivery frames seen:   %d\n", report.Count)
	fmt.Printf("delivery latency p50:   %s\n", report.P50)
	fmt.Printf("delivery latency p90:   %s\n", report.P90)
	fmt.Printf("delivery latency p99:   %s\n", report.P99)
	fmt.Printf("delivery latency max:   %s\n", report.Max)
}

func must(err error, action string) {
	if err != nil {
		log.Fatalf("%s: %v", action, err)
	}
}
