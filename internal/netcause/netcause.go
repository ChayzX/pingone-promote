// Package netcause names why an HTTPS request failed, in words an operator
// can act on, without quoting the error text (which can carry URLs and
// tenant details).
package netcause

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Describe returns a short cause for a failed request to host.
func Describe(err error, host string) string {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	var dns *net.DNSError
	var opErr *net.OpError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &unknown) || (errors.As(err, &verify) && strings.Contains(verify.Error(), "unknown authority")):
		return "the TLS certificate of " + host + " is not trusted. A company proxy that inspects HTTPS (Netskope, Zscaler, ...) signs it with the company's own root certificate: build the image with that certificate in deploy/container/extra-ca/ (docs/podman-mac.md)"
	case errors.As(err, &hostname):
		return "the TLS certificate does not belong to " + host + " (a proxy or a wrong address)"
	case errors.As(err, &invalid):
		return "the TLS certificate of " + host + " is not valid (expired or not yet valid; check the clock)"
	case errors.As(err, &dns):
		return host + " could not be found (DNS); check the address and that this machine can resolve it (a VPN-only host needs the VPN)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return host + " refused the connection; check the address and port"
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return host + " did not answer in time; check the network route and any proxy"
	case errors.As(err, &opErr):
		return "the connection to " + host + " failed; check the network route and any proxy"
	}
	return "the request to " + host + " failed; check the network route and any proxy"
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
