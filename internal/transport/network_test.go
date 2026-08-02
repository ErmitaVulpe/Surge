package transport

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"

	"github.com/SurgeDM/Surge/internal/types"
)

func TestNetworkPool_ClientSessionCache_SharedAcrossPoolKeys(t *testing.T) {
	pool := &NetworkPool{}

	t1 := pool.AcquireTransport("http://proxy1", "", 0)
	t2 := pool.AcquireTransport("http://proxy2", "", 0)
	defer pool.ReleaseTransport(t1)
	defer pool.ReleaseTransport(t2)

	if t1 == t2 {
		t.Fatal("expected distinct transports for different poolKeys")
	}
	if t1.TLSClientConfig == nil || t2.TLSClientConfig == nil {
		t.Fatal("expected non-nil TLSClientConfig on both transports")
	}
	if t1.TLSClientConfig == t2.TLSClientConfig {
		t.Fatal("expected distinct *tls.Config instances per transport")
	}
	if t1.TLSClientConfig.ClientSessionCache == nil {
		t.Fatal("expected non-nil ClientSessionCache")
	}
	if t1.TLSClientConfig.ClientSessionCache != t2.TLSClientConfig.ClientSessionCache {
		t.Fatal("expected shared ClientSessionCache pointer across poolKeys")
	}
	if t1.TLSClientConfig.ClientSessionCache != sharedClientSessionCache {
		t.Fatal("expected ClientSessionCache to be package sharedClientSessionCache")
	}
}

func TestNetworkPool_CloseAll_PreservesClientSessionCache(t *testing.T) {
	pool := &NetworkPool{}

	tr := pool.AcquireTransport("", "", 0)
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.ClientSessionCache == nil {
		t.Fatal("expected wired ClientSessionCache before CloseAll")
	}
	cache := tr.TLSClientConfig.ClientSessionCache
	pool.ReleaseTransport(tr)

	pool.CloseAll()

	tr2 := pool.AcquireTransport("", "", 0)
	defer pool.ReleaseTransport(tr2)

	if tr2.TLSClientConfig == nil {
		t.Fatal("expected non-nil TLSClientConfig after CloseAll re-Acquire")
	}
	if tr2.TLSClientConfig.ClientSessionCache != cache {
		t.Fatal("expected ClientSessionCache to survive CloseAll")
	}
	if tr2.TLSClientConfig.ClientSessionCache != sharedClientSessionCache {
		t.Fatal("expected surviving cache to remain package sharedClientSessionCache")
	}
	assertHTTP2Disabled(t, tr2)
}

func TestNetworkPool_ClientSessionCache_HTTP2Disabled(t *testing.T) {
	pool := &NetworkPool{}
	tr := pool.AcquireTransport("", "", 0)
	defer pool.ReleaseTransport(tr)
	assertHTTP2Disabled(t, tr)
}

func assertHTTP2Disabled(t *testing.T, tr *http.Transport) {
	t.Helper()
	if tr.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 == false")
	}
	if tr.TLSNextProto == nil {
		t.Error("expected non-nil TLSNextProto map")
	} else if len(tr.TLSNextProto) != 0 {
		t.Errorf("expected empty TLSNextProto, got len=%d", len(tr.TLSNextProto))
	}
	if tr.DialContext == nil {
		t.Error("expected custom DialContext")
	}
}

func TestNetworkPool_Reuse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := &NetworkPool{}
	runtime := &types.RuntimeConfig{}

	// First request
	transport1, _ := pool.AcquireTransport(runtime.ProxyURL, runtime.CustomDNS, 0, "", false)
	client1 := &http.Client{Transport: transport1}
	req1, _ := http.NewRequest("GET", server.URL, nil)
	resp1, err := client1.Do(req1)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}
	_ = resp1.Body.Close()
	pool.ReleaseTransport(transport1)

	// Second request with trace
	transport2, _ := pool.AcquireTransport(runtime.ProxyURL, runtime.CustomDNS, 0, "", false)
	client2 := &http.Client{Transport: transport2}
	reused := false
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				reused = true
			}
		},
	}
	req2, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), "GET", server.URL, nil)
	resp2, err := client2.Do(req2)
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}
	_ = resp2.Body.Close()
	pool.ReleaseTransport(transport2)

	if !reused {
		t.Error("Expected connection to be reused")
	}
}

func TestNetworkPool_IdleCleanup(t *testing.T) {
	pool := &NetworkPool{}
	runtime := &types.RuntimeConfig{}

	transport, _ := pool.AcquireTransport(runtime.ProxyURL, runtime.CustomDNS, 0, "", false)
	lease, ok := pool.transportMap[transport]
	if !ok {
		t.Fatal("Expected transport to be in transportMap")
	}

	if lease.refs != 1 {
		t.Errorf("Expected refs=1, got %d", lease.refs)
	}
	if lease.idleTimer != nil {
		t.Error("Expected no idle timer when refs > 0")
	}

	pool.ReleaseTransport(transport)
	if lease.refs != 0 {
		t.Errorf("Expected refs=0, got %d", lease.refs)
	}
	if lease.idleTimer == nil {
		t.Error("Expected idle timer to be started after ReleaseTransport()")
	}

	// Calling AcquireTransport again should stop the timer
	_, _ = pool.AcquireTransport(runtime.ProxyURL, runtime.CustomDNS, 0, "", false)
	if lease.idleTimer != nil {
		t.Error("Expected idle timer to be stopped after AcquireTransport()")
	}
	pool.ReleaseTransport(transport)
}

func TestNetworkPool_ConfigChange(t *testing.T) {
	pool := &NetworkPool{}

	r1 := &types.RuntimeConfig{ProxyURL: "http://proxy1"}
	t1, _ := pool.AcquireTransport(r1.ProxyURL, r1.CustomDNS, 0, "", false)
	pool.ReleaseTransport(t1)

	r2 := &types.RuntimeConfig{ProxyURL: "http://proxy2"}
	t2, _ := pool.AcquireTransport(r2.ProxyURL, r2.CustomDNS, 0, "", false)
	pool.ReleaseTransport(t2)

	if t1 == t2 {
		t.Error("Expected different transport after config change")
	}

	// Get with same config should reuse
	t3, _ := pool.AcquireTransport(r2.ProxyURL, r2.CustomDNS, 0, "", false)
	pool.ReleaseTransport(t3)

	if t2 != t3 {
		t.Error("Expected transport reuse for identical config")
	}
}

// TestNetworkPool_TLSKeyIsolation verifies that different TLS settings produce
// distinct pool entries and that insecure=true sets InsecureSkipVerify on the transport.
func TestNetworkPool_TLSKeyIsolation(t *testing.T) {
	pool := &NetworkPool{}

	plain, _ := pool.AcquireTransport("", "", 0, "", false)
	pool.ReleaseTransport(plain)

	insecure, _ := pool.AcquireTransport("", "", 0, "", true)
	pool.ReleaseTransport(insecure)

	if plain == insecure {
		t.Fatal("expected distinct transports for different TLS configs")
	}

	tlsCfg := insecure.TLSClientConfig
	if tlsCfg == nil {
		t.Fatal("expected TLSClientConfig to be set for insecure transport")
	}
	if !tlsCfg.InsecureSkipVerify { //nolint:gosec // intentional test assertion
		t.Error("expected InsecureSkipVerify=true on insecure transport")
	}

	// A plain transport must not skip verification.
	if cfg := plain.TLSClientConfig; cfg != nil {
		if cfg.InsecureSkipVerify { //nolint:gosec
			t.Error("plain transport must not have InsecureSkipVerify set")
		}
	}

	// Verify that invalid CA file returns an error
	withCA, err := pool.AcquireTransport("", "", 0, "nonexistent-but-distinct-key.pem", false)
	if err == nil {
		t.Error("expected error when CA file does not exist")
		pool.ReleaseTransport(withCA)
	}

	// Requesting the same key twice returns the same transport (pool reuse)
	a, _ := pool.AcquireTransport("", "", 0, "", true)
	b, _ := pool.AcquireTransport("", "", 0, "", true)
	pool.ReleaseTransport(a)
	pool.ReleaseTransport(b)
	if a != b {
		t.Error("expected pool to reuse transport for identical TLS config")
	}
	// Verify the insecure transport has the correct TLS config
	if a.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig on reused insecure transport")
	}
	cfg := a.TLSClientConfig
	if !cfg.InsecureSkipVerify { //nolint:gosec
		t.Error("reused insecure transport must still have InsecureSkipVerify=true")
	}
	_ = tls.Config{} // keep tls import used
}
