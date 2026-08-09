package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func validateHost(host string) error {
	normalized := strings.ToLower(strings.TrimSuffix(host, "."))
	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") {
		return newCodedError(fmt.Errorf("private host %q is not allowed", host), ErrorCodePrivateNetwork, "use a public URL or explicitly enable private networks for trusted local testing")
	}

	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return newCodedError(fmt.Errorf("private address %q is not allowed", host), ErrorCodePrivateNetwork, "use a public URL or explicitly enable private networks for trusted local testing")
		}
	}
	return nil
}

func (c *Client) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split dial address %q: %w", address, err)
	}
	if c.allowPrivateNetworks || net.ParseIP(host) != nil {
		return c.dialer(ctx, network, address)
	}

	resolver := c.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve host %q: no addresses returned", host)
	}
	for _, ip := range ips {
		if ip.IP == nil || isPrivateIP(ip.IP) {
			return nil, newCodedError(fmt.Errorf("host %q resolves to a private address", host), ErrorCodePrivateNetwork, "use a public URL or explicitly enable private networks for trusted local testing")
		}
	}

	dialErrors := make([]error, 0, len(ips))
	for _, ip := range ips {
		conn, err := c.dialer(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErrors = append(dialErrors, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(dialErrors...)
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectHops {
		return errRedirectLimit
	}
	if err := validateURL(req.Context(), req.URL, c.allowPrivateNetworks); err != nil {
		return err
	}
	if len(via) > 0 {
		sameRedirectOrigin := sameOrigin(via[0].URL, req.URL)
		for _, name := range []string{"Authorization", "X-Subscription-Token", "x-api-key"} {
			if sameRedirectOrigin {
				copyHeader(req.Header, via[0].Header, name)
			} else {
				req.Header.Del(name)
			}
		}
	}
	return nil
}

func copyHeader(dst, src http.Header, name string) {
	dst.Del(name)
	for _, value := range src.Values(name) {
		dst.Add(name, value)
	}
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(strings.TrimSuffix(left.Hostname(), "."), strings.TrimSuffix(right.Hostname(), ".")) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
