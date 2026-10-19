package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseConnect(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort string
		wantOK   bool
	}{
		// Canonical forms.
		{"connect 198.244.202.178:27015;", "198.244.202.178", "27015", true},
		{"connect 198.244.202.178:27015", "198.244.202.178", "27015", true},
		{"connect 198.244.202.178;", "198.244.202.178", "", true},
		{"connect 198.244.202.178", "198.244.202.178", "", true},
		{"  CONNECT 10.0.0.1:27015 ;", "10.0.0.1", "27015", true},
		{"connect play.example.com:27015;", "play.example.com", "27015", true},
		{"connect [2001:db8::1]:27015;", "2001:db8::1", "27015", true},
		{"connect 2001:db8::1", "2001:db8::1", "", true},
		// Loose: keyword/port/semicolon optional, address scanned out.
		{"198.244.202.178:27015", "198.244.202.178", "27015", true},
		{"please connect to 1.2.3.4:27015 thanks", "1.2.3.4", "27015", true},
		{"connect to 10.0.0.5", "10.0.0.5", "", true},
		{`connect "1.2.3.4:27015";`, "1.2.3.4", "27015", true},
		{"server is 5.6.7.8.", "5.6.7.8", "", true},
		{"play.example.com:27015", "play.example.com", "27015", true},
		{"play.example.com", "play.example.com", "", true},
		// Nothing address-like.
		{"connect;", "", "", false},
		{"connect   ", "", "", false},
		{"", "", "", false},
		{"connect bad host;", "", "", false},
	}
	for _, tt := range tests {
		host, port, ok := parseConnect(tt.in)
		if host != tt.wantHost || port != tt.wantPort || ok != tt.wantOK {
			t.Errorf("parseConnect(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, host, port, ok, tt.wantHost, tt.wantPort, tt.wantOK)
		}
	}
}

func TestProviderFromRDAP(t *testing.T) {
	tests := []struct {
		registrant, netname, want string
	}{
		{"OVH Ltd", "SD-ERI-lon1-eri1-sdagg25a-b-n93", "OVH"},
		{"HIVELOCITY, Inc.", "NET-107-155-64-0-1", "Hivelocity"},
		{"Hetzner Online GmbH", "HETZNER-fsn1-dc10", "Hetzner"},
		{"DigitalOcean, LLC", "DIGITALOCEAN-64-225-0-0", ""},
		{"Psychz Networks", "", "Psychz Networks"},
		{"", "CUST-CLOUDSERVER-LPG1-NET", "Latitude.sh"},
		{"", "DedicatedNodes-k798033", "i3D.net"}, // not Dedicated.com
		{"Amazon.com, Inc.", "AMZN-0-17", "AWS"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := providerFromRDAP(tt.registrant, tt.netname); got != tt.want {
			t.Errorf("providerFromRDAP(%q, %q) = %q, want %q",
				tt.registrant, tt.netname, got, tt.want)
		}
	}
}

func TestCitiesWithProvider(t *testing.T) {
	servers := map[string][]Server{
		"NA": {
			srv("Chicago", "Psychz Networks", "6.6.6.1"),
			srv("Dallas", "Psychz Networks", "6.6.6.2"),
			srv("Dallas", "OVH", "6.6.6.3"),
			srv("UK", "OVH", "6.6.6.4"),
		},
	}
	got := citiesWithProvider(servers, "Psychz Networks")
	if len(got) != 2 {
		t.Fatalf("got %d cities, want 2 (%+v)", len(got), got)
	}
	if got[0].Region != "NA" || got[0].City != "Chicago" || got[1].City != "Dallas" {
		t.Errorf("unexpected order: %+v", got)
	}
	if citiesWithProvider(servers, "nope") != nil && len(citiesWithProvider(servers, "nope")) != 0 {
		t.Error("unknown provider should match nothing")
	}
}

// stubLookupPTR replaces the PTR hook for the duration of a test.
func stubLookupPTR(t *testing.T, fn func(ctx context.Context, ip string) ([]string, error)) {
	t.Helper()
	orig := lookupPTR
	lookupPTR = fn
	t.Cleanup(func() { lookupPTR = orig })
}

func TestIdentifyTargetExactIP(t *testing.T) {
	stubLookupPTR(t, func(ctx context.Context, ip string) ([]string, error) {
		return nil, nil
	})
	// Point RDAP at a server that must NOT be contacted.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("RDAP should not be contacted for an exact-list IP")
	}))
	defer ts.Close()

	servers := map[string][]Server{"NA": {srv("Chicago", "Psychz Networks", "1.2.3.4")}}
	res := identifyTargetWith(context.Background(), "connect 1.2.3.4:27015;", servers, &rdapClient{base: ts.URL, http: ts.Client()})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Exact == nil || res.Exact.Provider != "Psychz Networks" {
		t.Fatalf("exact match = %+v, want Psychz Networks", res.Exact)
	}
	if res.Provider != "Psychz Networks" || res.IP != "1.2.3.4" || res.Port != "27015" {
		t.Errorf("result = %+v", res)
	}
}

func TestIdentifyTargetRDAP(t *testing.T) {
	stubLookupPTR(t, func(ctx context.Context, ip string) ([]string, error) {
		return []string{"speedtest.example."}, nil
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip/9.9.9.9":
			w.Write([]byte(`{
				"handle": "9.9.9.0 - 9.9.9.255",
				"name": "SD-ERI-lon1-test",
				"country": "GB",
				"entities": [{
					"handle": "OVH-MNT",
					"roles": ["registrant"],
					"vcardArray": ["vcard", [
						["fn", {}, "text", "OVH Ltd"],
						["org", {}, "text", "OVH Ltd"]
					]]
				}]
			}`))
		default:
			t.Errorf("unexpected RDAP path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	servers := map[string][]Server{"NA": {srv("Chicago", "Psychz Networks", "1.2.3.4")}}
	res := identifyTargetWith(context.Background(), "connect 9.9.9.9:27015;", servers, &rdapClient{base: ts.URL, http: ts.Client()})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Exact != nil {
		t.Errorf("exact = %+v, want nil", res.Exact)
	}
	if res.Registrant != "OVH Ltd" || res.Netname != "SD-ERI-lon1-test" || res.Country != "GB" {
		t.Errorf("rdap fields = %+v", res)
	}
	if res.Provider != "OVH" {
		t.Errorf("provider = %q, want OVH", res.Provider)
	}
	if len(res.PTR) != 1 || res.PTR[0] != "speedtest.example." {
		t.Errorf("ptr = %v", res.PTR)
	}
}

func TestIdentifyTargetEntityFollowUp(t *testing.T) {
	stubLookupPTR(t, func(ctx context.Context, ip string) ([]string, error) { return nil, nil })
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip/8.8.8.8":
			// RIPE style: registrant is only an ORG- handle.
			w.Write([]byte(`{
				"handle": "8.8.8.0 - 8.8.8.255",
				"name": "CUST-CLOUDSERVER-LPG1-NET",
				"country": "CH",
				"entities": [{"handle": "ORG-CA1567-RIPE", "roles": ["registrant"]}]
			}`))
		case "/entity/ORG-CA1567-RIPE":
			w.Write([]byte(`{
				"handle": "ORG-CA1567-RIPE",
				"vcardArray": ["vcard", [["org", {}, "text", "Latitude.sh"]]]
			}`))
		default:
			t.Errorf("unexpected RDAP path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	res := identifyTargetWith(context.Background(), "8.8.8.8", map[string][]Server{}, &rdapClient{base: ts.URL, http: ts.Client()})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Registrant != "Latitude.sh" {
		t.Errorf("registrant = %q, want Latitude.sh (entity follow-up)", res.Registrant)
	}
	if res.Provider != "Latitude.sh" {
		t.Errorf("provider = %q, want Latitude.sh", res.Provider)
	}
	if res.Netname != "CUST-CLOUDSERVER-LPG1-NET" {
		t.Errorf("netname = %q", res.Netname)
	}
}

func TestIdentifyTargetUnparseable(t *testing.T) {
	res := identifyTarget(context.Background(), "connect   ", map[string][]Server{})
	if res.Err == nil {
		t.Fatal("want parse error")
	}
}

func TestRdapClientTimeoutConfig(t *testing.T) {
	c := newRDAPClient()
	if c.base != rdapBase {
		t.Errorf("base = %q", c.base)
	}
	if c.http.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", c.http.Timeout)
	}
}

func TestPickIP(t *testing.T) {
	v6 := net.ParseIP("2001:db8::1")
	v4 := net.ParseIP("1.2.3.4")
	if got := pickIP([]net.IPAddr{{IP: v6}, {IP: v4}}); !got.Equal(v4) {
		t.Errorf("prefer v4: got %v", got)
	}
	if got := pickIP([]net.IPAddr{{IP: v6}}); !got.Equal(v6) {
		t.Errorf("v6 fallback: got %v", got)
	}
	if got := pickIP(nil); got != nil {
		t.Errorf("empty: got %v", got)
	}
}
