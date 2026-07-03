package upnp

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const igdV1Descriptor = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/ctl/IPConn</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`

const igdV2Descriptor = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:2</deviceType>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:2</deviceType>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType>
            <controlURL>/ctl/IPConn2</controlURL>
          </service>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
            <controlURL>/ctl/IPConn1</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`

func TestParseControlTarget(t *testing.T) {
	t.Run("igd v1 relative url resolved", func(t *testing.T) {
		target, err := parseControlTarget([]byte(igdV1Descriptor), "http://192.168.1.1:5000/desc.xml")
		require.NoError(t, err)
		assert.Equal(t, "http://192.168.1.1:5000/ctl/IPConn", target.URL)
		assert.Equal(t, "urn:schemas-upnp-org:service:WANIPConnection:1", target.ServiceType)
	})

	t.Run("igd v2 preferred over v1", func(t *testing.T) {
		target, err := parseControlTarget([]byte(igdV2Descriptor), "http://192.168.1.1:5000/desc.xml")
		require.NoError(t, err)
		assert.Equal(t, "http://192.168.1.1:5000/ctl/IPConn2", target.URL)
		assert.Equal(t, "urn:schemas-upnp-org:service:WANIPConnection:2", target.ServiceType)
	})

	t.Run("absolute control url kept", func(t *testing.T) {
		desc := `<root><device>
			<deviceType>x</deviceType>
			<serviceList><service>
				<serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
				<controlURL>http://10.0.0.1/ctl</controlURL>
			</service></serviceList>
		</device></root>`
		target, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.NoError(t, err)
		assert.Equal(t, "http://10.0.0.1/ctl", target.URL)
	})

	t.Run("urlbase is preferred for relative control url", func(t *testing.T) {
		desc := `<root>
			<URLBase>http://192.168.1.254:5000/base/</URLBase>
			<device>
				<serviceList><service>
					<serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
					<controlURL>ctl</controlURL>
				</service></serviceList>
			</device>
		</root>`
		target, err := parseControlTarget([]byte(desc), "http://192.168.1.1:5000/desc.xml")
		require.NoError(t, err)
		assert.Equal(t, "http://192.168.1.254:5000/base/ctl", target.URL)
	})

	t.Run("pppconnection accepted", func(t *testing.T) {
		desc := `<root><device>
			<serviceList><service>
				<serviceType>urn:schemas-upnp-org:service:WANPPPConnection:1</serviceType>
				<controlURL>/ppp</controlURL>
			</service></serviceList>
		</device></root>`
		target, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.NoError(t, err)
		assert.Equal(t, "http://192.168.1.1/ppp", target.URL)
	})

	t.Run("no wan service", func(t *testing.T) {
		desc := `<root><device><deviceType>x</deviceType></device></root>`
		_, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.Error(t, err)
	})

	t.Run("malformed xml", func(t *testing.T) {
		_, err := parseControlTarget([]byte("<root><device"), "http://192.168.1.1/desc.xml")
		require.Error(t, err)
	})

	t.Run("bad base url", func(t *testing.T) {
		_, err := parseControlTarget([]byte(igdV1Descriptor), "http://\x00bad")
		require.Error(t, err)
	})

	t.Run("empty control url", func(t *testing.T) {
		desc := `<root><device>
			<serviceList><service>
				<serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
				<controlURL> </controlURL>
			</service></serviceList>
		</device></root>`
		_, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.Error(t, err)
	})

	t.Run("bad control url", func(t *testing.T) {
		desc := "<root><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>http://\x00bad</controlURL></service></serviceList></device></root>"
		_, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.Error(t, err)
	})

	t.Run("bad urlbase", func(t *testing.T) {
		desc := "<root><URLBase>http://\x00bad</URLBase><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl</controlURL></service></serviceList></device></root>"
		_, err := parseControlTarget([]byte(desc), "http://192.168.1.1/desc.xml")
		require.Error(t, err)
	})
}

func TestResolveURL(t *testing.T) {
	base := &url.URL{Scheme: "http", Host: "192.168.1.1:5000", Path: "/desc.xml"}

	t.Run("bad reference", func(t *testing.T) {
		_, err := resolveURL(base, "%")
		require.Error(t, err)
	})
}

func TestDescriptorBase(t *testing.T) {
	t.Run("bad urlbase", func(t *testing.T) {
		_, err := descriptorBase("http://192.168.1.1/desc.xml", "%")
		require.Error(t, err)
	})
}
