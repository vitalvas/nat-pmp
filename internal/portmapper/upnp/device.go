// Package upnp implements a UPnP IGD (Internet Gateway Device) client for
// requesting port mappings. It discovers the gateway via SSDP, resolves the
// WAN connection service's control URL from the device descriptor, and issues
// SOAP actions over HTTP.
package upnp

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
)

// Service types for the WAN connection service, in preference order (IGDv2
// first, then IGDv1). Both WANIPConnection and WANPPPConnection are accepted.
var wanServiceTypes = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// deviceDescriptor is the root of a UPnP device description document.
type deviceDescriptor struct {
	XMLName xml.Name `xml:"root"`
	URLBase string   `xml:"URLBase"`
	Device  device   `xml:"device"`
}

type device struct {
	DeviceType  string    `xml:"deviceType"`
	ServiceList []service `xml:"serviceList>service"`
	DeviceList  []device  `xml:"deviceList>device"`
}

type service struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// controlTarget identifies a resolved WAN connection service.
type controlTarget struct {
	// URL is the absolute control URL for issuing SOAP actions.
	URL string
	// ServiceType is the service type used in SOAPAction headers.
	ServiceType string
}

// parseControlTarget parses a device descriptor and resolves the preferred WAN
// connection service into an absolute control URL. baseURL is the URL the
// descriptor was fetched from, used to resolve relative control URLs.
func parseControlTarget(descriptor []byte, baseURL string) (controlTarget, error) {
	var root deviceDescriptor
	if err := xml.Unmarshal(descriptor, &root); err != nil {
		return controlTarget{}, fmt.Errorf("upnp: parse descriptor: %w", err)
	}

	base, err := descriptorBase(baseURL, root.URLBase)
	if err != nil {
		return controlTarget{}, err
	}

	// Search the whole device tree, honoring the service-type preference order.
	for _, want := range wanServiceTypes {
		if svc, ok := findService(root.Device, want); ok {
			ctrl, err := resolveURL(base, svc.ControlURL)
			if err != nil {
				return controlTarget{}, err
			}
			return controlTarget{URL: ctrl, ServiceType: svc.ServiceType}, nil
		}
	}
	return controlTarget{}, fmt.Errorf("upnp: no WAN connection service found in descriptor")
}

// findService walks the device tree depth-first looking for a service whose
// type matches want.
func findService(d device, want string) (service, bool) {
	for _, svc := range d.ServiceList {
		if strings.EqualFold(svc.ServiceType, want) {
			return svc, true
		}
	}
	for _, child := range d.DeviceList {
		if svc, ok := findService(child, want); ok {
			return svc, true
		}
	}
	return service{}, false
}

// resolveURL turns a possibly-relative control URL into an absolute one.
func resolveURL(base *url.URL, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("upnp: empty control URL")
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("upnp: parse control URL %q: %w", ref, err)
	}
	return base.ResolveReference(u).String(), nil
}

func descriptorBase(location, rawURLBase string) (*url.URL, error) {
	base, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("upnp: parse base URL: %w", err)
	}
	rawURLBase = strings.TrimSpace(rawURLBase)
	if rawURLBase == "" {
		return base, nil
	}
	urlBase, err := url.Parse(rawURLBase)
	if err != nil {
		return nil, fmt.Errorf("upnp: parse URLBase %q: %w", rawURLBase, err)
	}
	return base.ResolveReference(urlBase), nil
}
