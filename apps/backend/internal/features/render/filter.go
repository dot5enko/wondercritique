package render

import (
	"context"
	"net/url"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// requestFilter is the per-variant network policy and record. It runs the CDP
// Fetch interception (allow everything the policy allows, fail and record
// everything else) and tracks Network events for the wait strategy and for
// blocks/failures that happen below the interception layer (content-security-
// policy blocks surface only as Network.loadingFailed with blockedReason=csp).
//
// A requestFilter belongs to exactly one variant/tab.
type requestFilter struct {
	policy *hostPolicy

	mu           sync.Mutex
	pausedOpen   int                          // Fetch pauses received but not yet answered
	netOpen      map[network.RequestID]string // Network requests in flight (id → URL)
	closed       map[network.RequestID]bool   // ids that already reached a terminal event
	fetchFailed  map[network.RequestID]bool   // ids we failed at the Fetch layer
	pendingBlock map[network.RequestID]string // blocked before its requestWillBeSent (reason)
	pendingFail  map[network.RequestID]string // failed before its requestWillBeSent (error)
	blocked      []BlockedRequest
	blockedSeen  map[string]bool
	failed       []FailedRequest
	failedSeen   map[string]bool
}

func newRequestFilter(p *hostPolicy) *requestFilter {
	return &requestFilter{
		policy:       p,
		netOpen:      map[network.RequestID]string{},
		closed:       map[network.RequestID]bool{},
		fetchFailed:  map[network.RequestID]bool{},
		pendingBlock: map[network.RequestID]string{},
		pendingFail:  map[network.RequestID]string{},
		blockedSeen:  map[string]bool{},
		failedSeen:   map[string]bool{},
	}
}

// start launches the event loops; they end when ctx (the tab) is done.
func (f *requestFilter) start(ctx context.Context) {
	go f.fetchLoop(ctx)
	go f.willBeSentLoop(ctx)
	go f.finishedLoop(ctx)
	go f.failedLoop(ctx)
}

// decide reports whether the URL may leave the browser through the proxy.
// data:/blob:/about: and other Chrome-internal schemes never hit the network
// and pass; everything else must be https on the default port to an
// allowlisted host. Non-https network schemes (http, ws, ftp, …) are refused:
// the egress proxy only tunnels CONNECT to port 443 anyway.
func (f *requestFilter) decide(raw string) (bool, string) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return false, "invalid-url"
	}
	switch u.Scheme {
	case "data", "blob", "about", "chrome", "chrome-error", "devtools", "filesystem", "view-source":
		return true, ""
	case "https":
		if u.User != nil {
			return false, "invalid-url"
		}
		if port := u.Port(); port != "" && port != "443" {
			return false, "port-not-allowed"
		}
		if !f.policy.allowsHost(u.Hostname()) {
			return false, "not-allowlisted"
		}
		return true, ""
	default:
		return false, "scheme-not-allowed"
	}
}

func (f *requestFilter) fetchLoop(ctx context.Context) {
	for ev, err := range chromedp.Events(ctx, fetch.RequestPaused) {
		if err != nil {
			return
		}
		f.mu.Lock()
		f.pausedOpen++
		f.mu.Unlock()

		raw := ev.Request.URL
		if allow, reason := f.decide(raw); allow {
			_ = chromedp.Do(ctx, cdpVoid(fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID}))
		} else {
			f.recordFetchBlocked(ev.NetworkID, raw, reason)
			_ = chromedp.Do(ctx, cdpVoid(fetch.FailRequest, fetch.FailRequestParams{
				RequestID:   ev.RequestID,
				ErrorReason: network.ErrorReasonBlockedByClient,
			}))
		}

		f.mu.Lock()
		f.pausedOpen--
		f.mu.Unlock()
	}
}

func (f *requestFilter) willBeSentLoop(ctx context.Context) {
	for ev, err := range chromedp.Events(ctx, network.RequestWillBeSent) {
		if err != nil {
			return
		}
		f.onWillBeSent(ev.RequestID, ev.Request.URL)
	}
}

func (f *requestFilter) finishedLoop(ctx context.Context) {
	for ev, err := range chromedp.Events(ctx, network.LoadingFinished) {
		if err != nil {
			return
		}
		f.onTerminal(ev.RequestID, "", false)
	}
}

func (f *requestFilter) failedLoop(ctx context.Context) {
	for ev, err := range chromedp.Events(ctx, network.LoadingFailed) {
		if err != nil {
			return
		}
		f.onTerminal(ev.RequestID, ev.ErrorText, ev.BlockedReason != "")
	}
}

func (f *requestFilter) recordFetchBlocked(networkID network.RequestID, raw, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if networkID != "" {
		f.fetchFailed[networkID] = true
	}
	f.recordBlockedLocked(raw, reason)
}

// onWillBeSent records a request entering the network stack. The event can
// arrive after its loadingFailed (the Network domain does not order events
// across methods), so a terminal event seen first is resolved here instead of
// being counted as in-flight forever.
func (f *requestFilter) onWillBeSent(id network.RequestID, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reason, ok := f.pendingBlock[id]; ok {
		delete(f.pendingBlock, id)
		f.closed[id] = true
		f.recordBlockedLocked(raw, reason)
		return
	}
	if errMsg, ok := f.pendingFail[id]; ok {
		delete(f.pendingFail, id)
		f.closed[id] = true
		f.recordFailedLocked(raw, errMsg)
		return
	}
	if f.closed[id] {
		return
	}
	f.netOpen[id] = raw // also refreshes the URL on redirects/retries
}

// onTerminal records a request leaving the network stack (finished or failed).
func (f *requestFilter) onTerminal(id network.RequestID, errorText string, blocked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url := f.netOpen[id]
	delete(f.netOpen, id)
	if f.fetchFailed[id] {
		// Already recorded as a Fetch-layer block; just close the id.
		delete(f.fetchFailed, id)
		f.closed[id] = true
		return
	}
	f.closed[id] = true
	if url == "" {
		// requestWillBeSent has not arrived yet; resolve when it does.
		if blocked {
			f.pendingBlock[id] = "content-security-policy"
		} else if errorText != "" {
			f.pendingFail[id] = errorText
		}
		return
	}
	if blocked {
		f.recordBlockedLocked(url, "content-security-policy")
	} else if errorText != "" {
		f.recordFailedLocked(url, errorText)
	}
}

func (f *requestFilter) recordBlockedLocked(raw, reason string) {
	if raw == "" || f.blockedSeen[raw] {
		return
	}
	f.blockedSeen[raw] = true
	if len(f.blocked) >= maxRecordedRequests {
		return
	}
	f.blocked = append(f.blocked, BlockedRequest{URL: raw, Reason: reason})
}

func (f *requestFilter) recordFailedLocked(raw, errorText string) {
	if raw == "" || f.failedSeen[raw] {
		return
	}
	f.failedSeen[raw] = true
	if len(f.failed) >= maxRecordedRequests {
		return
	}
	f.failed = append(f.failed, FailedRequest{URL: raw, Error: errorText})
}

// maxRecordedRequests bounds the metrics payload per variant.
const maxRecordedRequests = 50

// inFlight reports open Fetch pauses and in-flight network requests.
func (f *requestFilter) inFlight() (paused, pending int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pausedOpen, len(f.netOpen)
}

// snapshot returns the block/failure records for the variant's metrics. A URL
// that was blocked is never also listed as failed.
func (f *requestFilter) snapshot() (blocked []BlockedRequest, failed []FailedRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blocked = append([]BlockedRequest(nil), f.blocked...)
	for _, fr := range f.failed {
		if f.blockedSeen[fr.URL] {
			continue
		}
		failed = append(failed, fr)
	}
	return blocked, failed
}
