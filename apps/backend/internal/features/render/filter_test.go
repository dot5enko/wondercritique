package render

import "testing"

func TestFilterDecide(t *testing.T) {
	policy, err := newHostPolicy([]string{"cdn.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	f := newRequestFilter(policy)

	cases := []struct {
		url    string
		allow  bool
		reason string
	}{
		// Non-network schemes pass: they never leave the browser.
		{"data:image/gif;base64,R0lGOD", true, ""},
		{"blob:https://canvas.wondercritique.invalid/uuid", true, ""},
		{"about:blank", true, ""},
		{"chrome-error://chromewebdata/", true, ""},
		// https to allowlisted hosts passes (default port only).
		{"https://fonts.googleapis.com/css2?family=X", true, ""},
		{"https://fonts.gstatic.com/s/x.woff2", true, ""},
		{"https://cdn.example.com/logo.png", true, ""},
		{"https://CDN.EXAMPLE.COM/logo.png", true, ""},
		{"https://cdn.example.com:443/logo.png", true, ""},
		// Everything else is blocked with a stable reason.
		{"https://evil.example.com/x.png", false, "not-allowlisted"},
		{"http://cdn.example.com/logo.png", false, "scheme-not-allowed"},
		{"http://169.254.169.254/latest/meta-data/", false, "scheme-not-allowed"},
		{"ws://fonts.googleapis.com/x", false, "scheme-not-allowed"},
		{"ftp://fonts.gstatic.com/x", false, "scheme-not-allowed"},
		{"https://cdn.example.com:8443/logo.png", false, "port-not-allowed"},
		{"https://user:pass@cdn.example.com/x", false, "invalid-url"},
		{"%zz", false, "invalid-url"},
	}
	for _, tc := range cases {
		allow, reason := f.decide(tc.url)
		if allow != tc.allow || reason != tc.reason {
			t.Errorf("%s: allow=%v reason=%q, want allow=%v reason=%q",
				tc.url, allow, reason, tc.allow, tc.reason)
		}
	}
}

func TestFilterRecordsAndDedups(t *testing.T) {
	policy, err := newHostPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newRequestFilter(policy)

	// A Fetch-layer block records once, even if repeated.
	f.recordFetchBlocked("net-1", "https://evil.example/a.png", "not-allowlisted")
	f.recordFetchBlocked("net-1", "https://evil.example/a.png", "not-allowlisted")

	// A CSP-style terminal event before requestWillBeSent is resolved when the
	// request URL arrives, and does not stay in flight.
	f.onTerminal("net-2", "", true)
	if _, pending := f.inFlight(); pending != 0 {
		t.Fatalf("pending before willBeSent: %d", pending)
	}
	f.onWillBeSent("net-2", "https://blocked.example/b.png")

	// Normal lifecycle: open while in flight, closed after finishing.
	f.onWillBeSent("net-3", "https://fonts.gstatic.com/s.woff2")
	if paused, pending := f.inFlight(); pending != 1 {
		t.Fatalf("want 1 in flight, got paused=%d pending=%d", paused, pending)
	}
	f.onTerminal("net-3", "", false)
	if _, pending := f.inFlight(); pending != 0 {
		t.Fatalf("want 0 in flight, got %d", pending)
	}

	// A failure that arrives before its requestWillBeSent.
	f.onTerminal("net-4", "net::ERR_PROXY_CONNECTION_FAILED", false)
	f.onWillBeSent("net-4", "https://fonts.googleapis.com/css")

	// A Fetch-failed id must not be double-recorded as failed by loadingFailed.
	f.recordFetchBlocked("net-5", "https://evil.example/c.png", "not-allowlisted")
	f.onTerminal("net-5", "net::ERR_FAILED", false)

	blocked, failed := f.snapshot()
	if len(blocked) != 3 {
		t.Fatalf("blocked = %+v, want 3 entries", blocked)
	}
	if len(failed) != 1 || failed[0].URL != "https://fonts.googleapis.com/css" {
		t.Fatalf("failed = %+v, want the proxy failure only", failed)
	}
	if blocked[0].URL != "https://evil.example/a.png" || blocked[0].Reason != "not-allowlisted" {
		t.Fatalf("blocked[0] = %+v", blocked[0])
	}
	if blocked[1].URL != "https://blocked.example/b.png" || blocked[1].Reason != "content-security-policy" {
		t.Fatalf("blocked[1] = %+v", blocked[1])
	}
	if blocked[2].URL != "https://evil.example/c.png" {
		t.Fatalf("blocked[2] = %+v", blocked[2])
	}
}
