/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package caddy

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// DomainCheck holds the result of pre-flight domain validation.
type DomainCheck struct {
	Domain   string
	Valid    bool
	Warnings []string // non-fatal issues (e.g. slow DNS)
	Error    string   // fatal issue preventing HTTPS
}

// ValidateDomain runs pre-flight checks on a domain before adding it to
// the Caddy config. This catches common misconfigurations early and gives
// the user a clear message instead of a wall of ACME errors.
func ValidateDomain(domain string) *DomainCheck {
	result := &DomainCheck{Domain: domain, Valid: true}

	// Basic format checks.
	if domain == "" {
		result.Valid = false
		result.Error = "domain is empty"
		return result
	}
	if strings.Contains(domain, " ") {
		result.Valid = false
		result.Error = fmt.Sprintf("domain '%s' contains spaces", domain)
		return result
	}
	if strings.Contains(domain, "://") {
		result.Valid = false
		result.Error = fmt.Sprintf("domain should not include protocol (remove http:// or https:// from '%s')", domain)
		return result
	}
	if strings.Contains(domain, "/") {
		result.Valid = false
		result.Error = fmt.Sprintf("domain should not include a path (remove everything after the domain name in '%s')", domain)
		return result
	}

	// Localhost and IP addresses don't need ACME certificates.
	if domain == "localhost" || net.ParseIP(domain) != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("'%s' is a local/IP address, Let's Encrypt cannot issue certificates for it. HTTPS will use a self-signed certificate", domain))
		return result
	}

	// DNS resolution check.
	resolved, err := net.LookupHost(domain)
	if err != nil {
		result.Valid = false
		dnsErr, ok := err.(*net.DNSError)
		if ok && dnsErr.IsNotFound {
			result.Error = fmt.Sprintf("domain '%s' does not resolve to any IP address. Make sure your DNS A/AAAA record points to this server's IP", domain)
		} else if ok && dnsErr.IsTimeout {
			result.Error = fmt.Sprintf("DNS lookup for '%s' timed out. Check your DNS configuration or try again", domain)
		} else {
			result.Error = fmt.Sprintf("DNS lookup for '%s' failed: %v. Make sure the domain's DNS is configured correctly", domain, err)
		}
		return result
	}

	// Check if any resolved IP is likely this machine.
	// We can't be 100% sure, but we can warn if it resolves to a clearly wrong address.
	hasRoutable := false
	for _, ip := range resolved {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if !parsed.IsLoopback() && !parsed.IsUnspecified() {
			hasRoutable = true
		}
	}
	if !hasRoutable && len(resolved) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("domain '%s' resolves to %v which appears to be a loopback address. Let's Encrypt needs the domain to point to a public IP", domain, resolved))
	}

	// Port 80 check (required for HTTP-01 ACME challenge).
	conn, err := net.DialTimeout("tcp", ":80", 2*time.Second)
	if err != nil {
		result.Warnings = append(result.Warnings, "port 80 does not appear to be available on this machine. Let's Encrypt requires port 80 to be open for certificate verification (HTTP-01 challenge)")
	} else {
		conn.Close()
	}

	// Port 443 check.
	conn, err = net.DialTimeout("tcp", ":443", 2*time.Second)
	if err != nil {
		result.Warnings = append(result.Warnings, "port 443 does not appear to be available on this machine. HTTPS requires port 443 to be open")
	} else {
		conn.Close()
	}

	return result
}

// FormatCheckResult returns a human-readable summary of the domain check.
func (c *DomainCheck) FormatCheckResult() string {
	if c.Valid && len(c.Warnings) == 0 {
		return fmt.Sprintf("domain '%s' looks good", c.Domain)
	}

	var b strings.Builder
	if !c.Valid {
		b.WriteString(fmt.Sprintf("HTTPS setup for '%s' will fail: %s", c.Domain, c.Error))
	} else {
		b.WriteString(fmt.Sprintf("domain '%s' passed DNS check but has warnings:", c.Domain))
	}

	for _, w := range c.Warnings {
		b.WriteString("\n  * ")
		b.WriteString(w)
	}

	return b.String()
}
