package discovery

import (
	"fmt"
	"net"

	"github.com/grandcat/zeroconf"
)

// Broadcaster announces the harness daemon on the local LAN via mDNS.
type Broadcaster struct {
	server *zeroconf.Server
}

// NewBroadcaster registers a _harness._tcp service. It advertises the SPKI
// pin (not the certificate) so clients can pin the host identity across
// certificate regeneration.
func NewBroadcaster(instance string, port int, spki string) (*Broadcaster, error) {
	txt := []string{
		"version=1",
		"path=/ws",
		"spkialg=sha256",
		fmt.Sprintf("spki=%s", spki),
	}
	server, err := zeroconf.Register(instance, "_harness._tcp", "local.", port, txt, nil)
	if err != nil {
		return nil, err
	}
	return &Broadcaster{server: server}, nil
}

// Shutdown stops the mDNS announcement.
func (b *Broadcaster) Shutdown() {
	if b.server != nil {
		b.server.Shutdown()
	}
}

// AllInterfaces returns every non-loopback interface for discovery binding.
func AllInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, iface)
	}
	return out
}
