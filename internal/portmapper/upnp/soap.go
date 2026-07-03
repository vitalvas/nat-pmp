package upnp

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"

	"github.com/vitalvas/nat-pmp/internal/portmapper"
)

// httpDoer is the subset of *http.Client used by the client, abstracted so
// tests can inject a fake HTTP transport.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// UPnP IGD SOAP error codes that map to sentinel errors (UPnP-IGD spec).
const (
	errConflictInMapping   = 718
	errNoSuchEntry         = 714
	errActionNotAuthorized = 606
	errOutOfResources      = 728
)

// soapArg is a single named argument in a SOAP action.
type soapArg struct {
	Name  string
	Value string
}

// soapFault holds the decoded UPnP error out of a SOAP fault body.
type soapFault struct {
	Code        int
	Description string
}

// callSOAP issues a SOAP action against the control URL and returns the raw
// response body on success, or a decoded fault error.
func callSOAP(ctx context.Context, doer httpDoer, ctrl controlTarget, action string, args []soapArg) ([]byte, error) {
	body := buildEnvelope(ctrl.ServiceType, action, args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ctrl.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("upnp: build request: %w", err)
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, ctrl.ServiceType, action))

	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp: http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("upnp: read response: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		return respBody, nil
	}

	fault, ferr := parseFault(respBody)
	if ferr != nil {
		return nil, fmt.Errorf("upnp: action %s failed with status %d", action, resp.StatusCode)
	}
	return nil, faultError(action, fault)
}

// faultError maps a decoded SOAP fault to a sentinel error where possible.
func faultError(action string, f soapFault) error {
	switch f.Code {
	case errConflictInMapping:
		return fmt.Errorf("%w: %s", portmapper.ErrConflict, f.Description)
	case errOutOfResources:
		return fmt.Errorf("%w: %s", portmapper.ErrNoResources, f.Description)
	case errActionNotAuthorized:
		return fmt.Errorf("%w: %s", portmapper.ErrNotAuthorized, f.Description)
	case errNoSuchEntry:
		if action == "DeletePortMapping" {
			// Deleting a non-existent mapping is not an error for our purposes.
			return nil
		}
		return fmt.Errorf("upnp: SOAP fault %d: %s", f.Code, f.Description)
	default:
		return fmt.Errorf("upnp: SOAP fault %d: %s", f.Code, f.Description)
	}
}

// buildEnvelope constructs a SOAP request envelope for the given action.
func buildEnvelope(serviceType, action string, args []soapArg) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `)
	b.WriteString(`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	b.WriteString(`<s:Body>`)
	fmt.Fprintf(&b, `<u:%s xmlns:u="%s">`, action, serviceType)
	for _, arg := range args {
		fmt.Fprintf(&b, `<%s>%s</%s>`, arg.Name, escapeXML(arg.Value), arg.Name)
	}
	fmt.Fprintf(&b, `</u:%s>`, action)
	b.WriteString(`</s:Body></s:Envelope>`)
	return b.Bytes()
}

func escapeXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// faultDoc mirrors the nested structure of a UPnP SOAP fault response.
type faultDoc struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		Fault struct {
			Detail struct {
				UPnPError struct {
					ErrorCode        int    `xml:"errorCode"`
					ErrorDescription string `xml:"errorDescription"`
				} `xml:"UPnPError"`
			} `xml:"detail"`
		} `xml:"Fault"`
	} `xml:"Body"`
}

// parseFault extracts the UPnP error code and description from a SOAP fault.
func parseFault(body []byte) (soapFault, error) {
	var doc faultDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return soapFault{}, err
	}
	upnpErr := doc.Body.Fault.Detail.UPnPError
	if upnpErr.ErrorCode == 0 {
		return soapFault{}, fmt.Errorf("upnp: no UPnP error code in fault")
	}
	return soapFault{Code: upnpErr.ErrorCode, Description: upnpErr.ErrorDescription}, nil
}

// parseStringField extracts the text of the first element with the given local
// name from a SOAP response body.
func parseStringField(body []byte, field string) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != field {
			continue
		}
		var value string
		if err := dec.DecodeElement(&value, &start); err != nil {
			return "", err
		}
		return value, nil
	}
	return "", fmt.Errorf("upnp: field %q not found in response", field)
}
