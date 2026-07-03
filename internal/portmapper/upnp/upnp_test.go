package upnp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/mapping"
	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

const addMappingResponse = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
  </s:Body>
</s:Envelope>`

const deleteMappingResponse = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
  </s:Body>
</s:Envelope>`

func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

// soapRouter dispatches SOAP requests by the action embedded in the body.
func soapRouter(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		for name, resp := range responses {
			if strings.Contains(action, name) {
				_, _ = io.WriteString(w, resp)
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, faultResponse(501, "unknown action"))
	}))
}

// newClientWithServer builds a client whose descriptor points at srv and whose
// SSDP/fetch are stubbed to return a descriptor referencing srv's control URL.
func newClientWithServer(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	descriptor := `<root><device>
		<serviceList><service>
			<serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
			<controlURL>/ctl</controlURL>
		</service></serviceList>
	</device></root>`

	location := fmt.Sprintf("%s/desc.xml", srv.URL)
	controlURL := fmt.Sprintf("%s/ctl", srv.URL)
	base := make([]Option, 0, 4+len(opts))
	base = append(base,
		WithHTTPClient(srv.Client()),
		WithClock(fixedClock()),
		WithSearch(func(_ context.Context) ([]ssdpResult, error) {
			return []ssdpResult{{Location: location}}, nil
		}),
		WithFetch(func(_ context.Context, _ string) ([]byte, error) {
			// Rewrite the control URL to point at the test server.
			return []byte(strings.Replace(descriptor, "/ctl", controlURL, 1)), nil
		}),
	)
	return New(append(base, opts...)...)
}

func TestDefaultHTTPTimeout(t *testing.T) {
	client, ok := New().doer.(*http.Client)
	require.True(t, ok)
	assert.Equal(t, defaultHTTPTimeout, client.Timeout)
}

func TestExternalIP(t *testing.T) {
	srv := soapRouter(t, map[string]string{"GetExternalIPAddress": extIPResponse})
	defer srv.Close()

	c := newClientWithServer(t, srv)
	ip, err := c.ExternalIP(context.Background())
	require.NoError(t, err)
	assert.Equal(t, netip.MustParseAddr("203.0.113.42"), ip)
}

func TestExternalIPBadAddress(t *testing.T) {
	badResp := strings.Replace(extIPResponse, "203.0.113.42", "not-an-ip", 1)
	srv := soapRouter(t, map[string]string{"GetExternalIPAddress": badResp})
	defer srv.Close()

	c := newClientWithServer(t, srv)
	_, err := c.ExternalIP(context.Background())
	require.Error(t, err)
}

func TestExternalIPMissingField(t *testing.T) {
	resp := strings.Replace(extIPResponse, "NewExternalIPAddress", "OtherField", 2)
	srv := soapRouter(t, map[string]string{"GetExternalIPAddress": resp})
	defer srv.Close()

	c := newClientWithServer(t, srv)
	_, err := c.ExternalIP(context.Background())
	require.Error(t, err)
}

func TestMap(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := soapRouter(t, map[string]string{
			"AddPortMapping":       addMappingResponse,
			"GetExternalIPAddress": extIPResponse,
		})
		defer srv.Close()

		c := newClientWithServer(t, srv)
		c.SetInternalClient(netip.MustParseAddr("192.168.1.50"))
		lease, err := c.Map(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
			Lease:        time.Hour,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(30000), lease.ExternalPort)
		assert.Equal(t, netip.MustParseAddr("203.0.113.42"), lease.ExternalIP)
		assert.Equal(t, time.Hour, lease.Lifetime)
	})

	t.Run("zero external port defaults to internal", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(r.Header.Get("SOAPAction"), "AddPortMapping") {
				gotBody = string(body)
				_, _ = io.WriteString(w, addMappingResponse)
				return
			}
			_, _ = io.WriteString(w, extIPResponse)
		}))
		defer srv.Close()

		c := newClientWithServer(t, srv)
		c.SetInternalClient(netip.MustParseAddr("192.168.1.50"))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.UDP, InternalPort: 51820})
		require.NoError(t, err)
		assert.Contains(t, gotBody, "<NewExternalPort>51820</NewExternalPort>")
		assert.Contains(t, gotBody, "<NewProtocol>UDP</NewProtocol>")
	})

	t.Run("internal client is required", func(t *testing.T) {
		srv := soapRouter(t, map[string]string{
			"AddPortMapping":       addMappingResponse,
			"GetExternalIPAddress": extIPResponse,
		})
		defer srv.Close()

		c := newClientWithServer(t, srv)
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "internal client")
	})

	t.Run("invalid request", func(t *testing.T) {
		c := New()
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 0})
		require.Error(t, err)
	})

	t.Run("bad protocol", func(t *testing.T) {
		c := New()
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.Protocol("x"), InternalPort: 1})
		require.Error(t, err)
	})

	t.Run("conflict surfaces", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, faultResponse(errConflictInMapping, "conflict"))
		}))
		defer srv.Close()

		c := newClientWithServer(t, srv)
		c.SetInternalClient(netip.MustParseAddr("192.168.1.50"))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		assert.ErrorIs(t, err, portmapper.ErrConflict)
	})

	t.Run("resolve target error", func(t *testing.T) {
		c := New(WithSearch(func(context.Context) ([]ssdpResult, error) {
			return nil, assert.AnError
		}))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("soap error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, faultResponse(501, "boom"))
		}))
		defer srv.Close()

		c := newClientWithServer(t, srv)
		c.SetInternalClient(netip.MustParseAddr("192.168.1.50"))
		_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})
}

func TestUnmap(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := soapRouter(t, map[string]string{"DeletePortMapping": deleteMappingResponse})
		defer srv.Close()

		c := newClientWithServer(t, srv)
		err := c.Unmap(context.Background(), mapping.Request{
			Protocol:     mapping.TCP,
			InternalPort: 22000,
			ExternalPort: 30000,
		})
		require.NoError(t, err)
	})

	t.Run("bad protocol", func(t *testing.T) {
		c := New()
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.Protocol("x"), InternalPort: 1})
		require.Error(t, err)
	})

	t.Run("invalid request", func(t *testing.T) {
		c := New()
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 0})
		require.Error(t, err)
	})

	t.Run("resolve target error", func(t *testing.T) {
		c := New(WithSearch(func(context.Context) ([]ssdpResult, error) {
			return nil, assert.AnError
		}))
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("soap error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, faultResponse(501, "boom"))
		}))
		defer srv.Close()

		c := newClientWithServer(t, srv)
		err := c.Unmap(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
		require.Error(t, err)
	})
}

func TestResolveTarget(t *testing.T) {
	t.Run("caches after first resolve", func(t *testing.T) {
		calls := 0
		srv := soapRouter(t, map[string]string{"GetExternalIPAddress": extIPResponse})
		defer srv.Close()

		c := newClientWithServer(t, srv, WithSearch(func(_ context.Context) ([]ssdpResult, error) {
			calls++
			return []ssdpResult{{Location: fmt.Sprintf("%s/desc.xml", srv.URL)}}, nil
		}))
		_, err := c.ExternalIP(context.Background())
		require.NoError(t, err)
		_, err = c.ExternalIP(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "SSDP search should run once and cache")
	})

	t.Run("search error", func(t *testing.T) {
		c := New(WithSearch(func(_ context.Context) ([]ssdpResult, error) {
			return nil, assert.AnError
		}))
		_, err := c.ExternalIP(context.Background())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("fetch error then no usable descriptor", func(t *testing.T) {
		c := New(
			WithSearch(func(_ context.Context) ([]ssdpResult, error) {
				return []ssdpResult{{Location: "http://x/d"}}, nil
			}),
			WithFetch(func(_ context.Context, _ string) ([]byte, error) {
				return nil, assert.AnError
			}),
		)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("descriptor without wan service", func(t *testing.T) {
		c := New(
			WithSearch(func(_ context.Context) ([]ssdpResult, error) {
				return []ssdpResult{{Location: "http://x/d"}}, nil
			}),
			WithFetch(func(_ context.Context, _ string) ([]byte, error) {
				return []byte(`<root><device><deviceType>x</deviceType></device></root>`), nil
			}),
		)
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})

	t.Run("empty search results", func(t *testing.T) {
		c := New(WithSearch(func(context.Context) ([]ssdpResult, error) {
			return nil, nil
		}))
		_, err := c.ExternalIP(context.Background())
		require.Error(t, err)
	})
}

func TestFetchHTTP(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "descriptor")
		}))
		defer srv.Close()

		c := New(WithHTTPClient(srv.Client()))
		body, err := c.fetchHTTP(context.Background(), srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "descriptor", string(body))
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(WithHTTPClient(srv.Client()))
		_, err := c.fetchHTTP(context.Background(), srv.URL)
		require.Error(t, err)
	})

	t.Run("bad url", func(t *testing.T) {
		c := New()
		_, err := c.fetchHTTP(context.Background(), "http://\x00bad")
		require.Error(t, err)
	})

	t.Run("transport error", func(t *testing.T) {
		c := New()
		_, err := c.fetchHTTP(context.Background(), "http://127.0.0.1:0/")
		require.Error(t, err)
	})
}

func TestProtocolString(t *testing.T) {
	tcp, err := protocolString(mapping.TCP)
	require.NoError(t, err)
	assert.Equal(t, "TCP", tcp)

	udp, err := protocolString(mapping.UDP)
	require.NoError(t, err)
	assert.Equal(t, "UDP", udp)

	_, err = protocolString(mapping.Protocol("x"))
	require.Error(t, err)
}

func TestSetInternalClient(t *testing.T) {
	c := New()
	c.SetInternalClient(netip.MustParseAddr("192.168.1.99"))
	assert.Equal(t, "192.168.1.99", c.getInternalClient())

	c.SetInternalClient(netip.Addr{})
	assert.Empty(t, c.getInternalClient())

	c.SetInternalClient(netip.IPv4Unspecified())
	assert.Empty(t, c.getInternalClient())
}

func TestWithSearchTimeout(t *testing.T) {
	c := New(WithSearchTimeout(5 * time.Second))
	assert.Equal(t, 5*time.Second, c.searchTimeout)
}

func TestMapExternalIPFailure(t *testing.T) {
	// AddPortMapping succeeds but the follow-up GetExternalIPAddress faults.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPAction"), "AddPortMapping") {
			_, _ = io.WriteString(w, addMappingResponse)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, faultResponse(501, "boom"))
	}))
	defer srv.Close()

	c := newClientWithServer(t, srv)
	c.SetInternalClient(netip.MustParseAddr("192.168.1.50"))
	_, err := c.Map(context.Background(), mapping.Request{Protocol: mapping.TCP, InternalPort: 22000})
	require.Error(t, err)
}
