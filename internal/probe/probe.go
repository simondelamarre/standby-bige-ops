// Package probe checks that a machine or the app is ready.
package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/manifest"
)

const timeout = 5 * time.Second

var client = &http.Client{
	Timeout: timeout,
	// A redirect is an answer: the service is up.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Check returns nil when the probe target answers.
func Check(ctx context.Context, p manifest.Probe) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch p.Kind {
	case "tcp":
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", p.Target)
		if err != nil {
			return err
		}
		return conn.Close()
	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Target, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("%s answered HTTP %d", p.Target, resp.StatusCode)
		}
		return nil
	}
	return fmt.Errorf("unknown probe kind %q", p.Kind)
}
