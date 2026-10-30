// Package docker drives containers through the Docker Engine API. Local tests only:
// access to the Docker socket is root-equivalent on the host.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

type Driver struct {
	client *http.Client
	base   string
	// StopTimeout is the grace period, in seconds, before Docker kills a container.
	StopTimeout int
}

// NewUnix talks to the Docker Engine over its unix socket.
func NewUnix(socket string) *Driver {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return New(&http.Client{Transport: tr, Timeout: 90 * time.Second}, "http://docker")
}

// New uses any HTTP client and base URL; tests point it at a fake engine.
func New(client *http.Client, base string) *Driver {
	return &Driver{client: client, base: base, StopTimeout: 60}
}

func (d *Driver) States(ctx context.Context, ids []string) (map[string]driver.State, error) {
	out := make(map[string]driver.State, len(ids))
	for _, id := range ids {
		state, err := d.state(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, nil
}

func (d *Driver) state(ctx context.Context, id string) (driver.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return driver.Missing, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker: inspect %s: HTTP %d", id, resp.StatusCode)
	}
	var body struct {
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("docker: inspect %s: %w", id, err)
	}
	switch body.State.Status {
	case "running":
		return driver.Running, nil
	case "created", "exited", "dead":
		return driver.Stopped, nil
	default:
		return driver.Changing, nil
	}
}

func (d *Driver) Start(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := d.post(ctx, "/containers/"+url.PathEscape(id)+"/start"); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) Stop(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := d.post(ctx, "/containers/"+url.PathEscape(id)+"/stop?t="+strconv.Itoa(d.StopTimeout)); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 204: done; 304: already in the requested state.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("docker: POST %s: HTTP %d: %s", path, resp.StatusCode, msg)
}
