package telemetry

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

// setAuth adds HTTP basic auth from <prefix>_USERNAME and <prefix>_PASSWORD
// (for example Grafana Cloud: instance ID and an access policy token).
// Credentials are never sent over plain HTTP to a non-loopback host.
func setAuth(req *http.Request, prefix string) error {
	user, pass := os.Getenv(prefix+"_USERNAME"), os.Getenv(prefix+"_PASSWORD")
	if user == "" && pass == "" {
		return nil
	}
	if user == "" || pass == "" {
		return fmt.Errorf("%s_USERNAME and %s_PASSWORD must be set together", prefix, prefix)
	}
	if req.URL.Scheme != "https" {
		if ip := net.ParseIP(req.URL.Hostname()); req.URL.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("refusing to send %s credentials over plain HTTP; use an https URL", prefix)
		}
	}
	req.SetBasicAuth(user, pass)
	return nil
}
