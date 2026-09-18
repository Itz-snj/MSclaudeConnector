package netutil

import "net"

// LANIPs returns non-loopback IPv4 addresses of up network interfaces.
// If no addresses are found, it falls back to ["127.0.0.1"] so local testing
// still works.
func LANIPs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	if len(ips) == 0 {
		ips = []string{"127.0.0.1"}
	}
	return ips, nil
}
