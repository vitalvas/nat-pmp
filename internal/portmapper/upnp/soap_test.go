package upnp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

const extIPResponse = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>203.0.113.42</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`

func faultResponse(code int, desc string) string {
	const tmpl = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <s:Fault>
      <faultcode>s:Client</faultcode>
      <faultstring>UPnPError</faultstring>
      <detail>
        <UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
          <errorCode>%d</errorCode>
          <errorDescription>%s</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`
	return fmt.Sprintf(tmpl, code, desc)
}

func testTarget(url string) controlTarget {
	return controlTarget{URL: url, ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1"}
}

func TestCallSOAP(t *testing.T) {
	t.Run("success and headers", func(t *testing.T) {
		var gotAction, gotContentType string
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAction = r.Header.Get("SOAPAction")
			gotContentType = r.Header.Get("Content-Type")
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			_, _ = io.WriteString(w, extIPResponse)
		}))
		defer srv.Close()

		body, err := callSOAP(context.Background(), srv.Client(), testTarget(srv.URL),
			"GetExternalIPAddress", nil)
		require.NoError(t, err)
		assert.Contains(t, string(body), "203.0.113.42")
		assert.Equal(t, `"urn:schemas-upnp-org:service:WANIPConnection:1#GetExternalIPAddress"`, gotAction)
		assert.Contains(t, gotContentType, "text/xml")
		assert.Contains(t, gotBody, "<u:GetExternalIPAddress")
	})

	t.Run("args serialized and escaped", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			_, _ = io.WriteString(w, extIPResponse)
		}))
		defer srv.Close()

		_, err := callSOAP(context.Background(), srv.Client(), testTarget(srv.URL), "AddPortMapping",
			[]soapArg{{Name: "NewPortMappingDescription", Value: "a & b"}})
		require.NoError(t, err)
		assert.Contains(t, gotBody, "a &amp; b")
	})

	t.Run("conflict fault", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, faultResponse(errConflictInMapping, "conflict"))
		}))
		defer srv.Close()

		_, err := callSOAP(context.Background(), srv.Client(), testTarget(srv.URL), "AddPortMapping", nil)
		assert.ErrorIs(t, err, portmapper.ErrConflict)
	})

	t.Run("no-such-entry fault is not an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, faultResponse(errNoSuchEntry, "gone"))
		}))
		defer srv.Close()

		_, err := callSOAP(context.Background(), srv.Client(), testTarget(srv.URL), "DeletePortMapping", nil)
		assert.NoError(t, err)
	})

	t.Run("unparseable fault falls back to status error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "not xml")
		}))
		defer srv.Close()

		_, err := callSOAP(context.Background(), srv.Client(), testTarget(srv.URL), "AddPortMapping", nil)
		require.Error(t, err)
	})

	t.Run("transport error", func(t *testing.T) {
		_, err := callSOAP(context.Background(), http.DefaultClient,
			testTarget("http://127.0.0.1:0/ctl"), "GetExternalIPAddress", nil)
		require.Error(t, err)
	})

	t.Run("bad url", func(t *testing.T) {
		_, err := callSOAP(context.Background(), http.DefaultClient,
			testTarget("http://\x00bad/"), "GetExternalIPAddress", nil)
		require.Error(t, err)
	})

	t.Run("read response error", func(t *testing.T) {
		doer := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       errBody{},
			}, nil
		})
		_, err := callSOAP(context.Background(), doer, testTarget("http://example.test/ctl"), "GetExternalIPAddress", nil)
		require.Error(t, err)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, assert.AnError }
func (errBody) Close() error             { return nil }

func TestFaultError(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{name: "conflict", code: errConflictInMapping, want: portmapper.ErrConflict},
		{name: "out of resources", code: errOutOfResources, want: portmapper.ErrNoResources},
		{name: "not authorized", code: errActionNotAuthorized, want: portmapper.ErrNotAuthorized},
		{name: "no such entry", code: errNoSuchEntry, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := faultError("DeletePortMapping", soapFault{Code: tt.code, Description: "d"})
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}

	t.Run("unknown code is generic", func(t *testing.T) {
		err := faultError("DeletePortMapping", soapFault{Code: 999, Description: "weird"})
		require.Error(t, err)
	})

	t.Run("no such entry is only ignored for deletes", func(t *testing.T) {
		err := faultError("AddPortMapping", soapFault{Code: errNoSuchEntry, Description: "gone"})
		require.Error(t, err)
	})
}

func TestParseFault(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		f, err := parseFault([]byte(faultResponse(718, "conflict")))
		require.NoError(t, err)
		assert.Equal(t, 718, f.Code)
		assert.Equal(t, "conflict", f.Description)
	})

	t.Run("no error code", func(t *testing.T) {
		body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body></s:Body></s:Envelope>`
		_, err := parseFault([]byte(body))
		require.Error(t, err)
	})

	t.Run("malformed", func(t *testing.T) {
		_, err := parseFault([]byte("<bad"))
		require.Error(t, err)
	})
}

func TestParseStringField(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		v, err := parseStringField([]byte(extIPResponse), "NewExternalIPAddress")
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.42", v)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := parseStringField([]byte(extIPResponse), "NoSuchField")
		require.Error(t, err)
	})

	t.Run("malformed xml", func(t *testing.T) {
		_, err := parseStringField([]byte("<a><b"), "b")
		require.Error(t, err)
	})

	t.Run("decode element error", func(t *testing.T) {
		body := []byte("<root><NewExternalIPAddress><nested></NewExternalIPAddress></root>")
		_, err := parseStringField(body, "NewExternalIPAddress")
		require.Error(t, err)
	})
}

func TestBuildEnvelope(t *testing.T) {
	env := buildEnvelope("urn:svc:1", "DoThing", []soapArg{{Name: "K", Value: "V"}})
	s := string(env)
	assert.Contains(t, s, `<u:DoThing xmlns:u="urn:svc:1">`)
	assert.Contains(t, s, "<K>V</K>")
	assert.Contains(t, s, "</u:DoThing>")
}
