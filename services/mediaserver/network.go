package mediaserver

import (
	"context"
	"fmt"
	"net"
	"net/url"
)

// deviceListener opens where devices on this machine's network reach its embedded media server.
func (c *Config) deviceListener(ctx context.Context) (net.Listener, error) {
	ip, err := lanIPv4(ctx, c.Network.Interface)
	if err != nil {
		return nil, err
	}
	return net.Listen("tcp", net.JoinHostPort(ip, "0"))
}

// advertised is where devices reach a server listening on l: server.advertise, or l's port on this machine's network.
func (c *Config) advertised(ctx context.Context, l net.Listener) (*url.URL, error) {
	if c.Server.Advertise != "" {
		return url.Parse(c.Server.Advertise)
	}
	ip, err := lanIPv4(ctx, c.Network.Interface)
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return nil, err
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(ip, port)}, nil
}

// lanIPv4 is this machine's address on its network: the pinned interface's, or the default route's.
func lanIPv4(ctx context.Context, iface string) (string, error) {
	if iface == "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", "8.8.8.8:53")
		if err != nil {
			return "", fmt.Errorf("detecting default-route address (set network.interface to pin one): %w", err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
	}

	pinned, err := net.InterfaceByName(iface)
	if err != nil {
		return "", fmt.Errorf("looking up interface %q: %w", iface, err)
	}
	addrs, err := pinned.Addrs()
	if err != nil {
		return "", fmt.Errorf("listing addresses on %s: %w", iface, err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil && !ip.IsLoopback() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address on %s", iface)
}
