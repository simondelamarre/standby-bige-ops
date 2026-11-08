// Package ovh drives OVHcloud Public Cloud instances through the OVH API: it
// lists them, then shelves, unshelves or starts them. It never deletes an
// instance and calls nothing else.
package ovh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

// tokenMargin renews a token this long before it expires.
const tokenMargin = time.Minute

var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

var endpoints = map[string]struct{ api, token string }{
	"ovh-eu": {"https://eu.api.ovh.com/1.0", "https://www.ovh.com/auth/oauth2/token"},
	"ovh-ca": {"https://ca.api.ovh.com/1.0", "https://ca.ovh.com/auth/oauth2/token"},
	"ovh-us": {"https://api.us.ovhcloud.com/1.0", "https://us.ovhcloud.com/auth/oauth2/token"},
}

// ValidEndpoint says whether name is ovh-eu, ovh-ca or ovh-us.
func ValidEndpoint(name string) bool {
	_, ok := endpoints[name]
	return ok
}

// ValidServiceName says whether a project id is safe to put in an API path.
func ValidServiceName(name string) bool {
	return serviceNameRe.MatchString(name)
}

// Credentials is an OAuth2 client (service account). It is never logged.
type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// LoadCredentials reads a JSON OAuth2 client from a file only its owner can read.
func LoadCredentials(path string) (Credentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Credentials{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, fmt.Errorf("%s must be readable by its owner only (chmod 600)", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, fmt.Errorf("%s: not a JSON OAuth2 client", path)
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return Credentials{}, fmt.Errorf("%s: client_id and client_secret are required", path)
	}
	return c, nil
}

type Driver struct {
	client   *http.Client
	endpoint string
	tokenURL string
	service  string
	creds    Credentials
	now      func() time.Time

	// The reconcile tick and a wake-up share the token.
	mu     sync.Mutex
	token  string
	expiry time.Time
}

// New targets a project on the API of endpoint: ovh-eu, ovh-ca or ovh-us.
func New(endpoint, serviceName string, creds Credentials) (*Driver, error) {
	target, ok := endpoints[endpoint]
	if !ok {
		return nil, fmt.Errorf("ovh: invalid endpoint %q", endpoint)
	}
	if !ValidServiceName(serviceName) {
		return nil, fmt.Errorf("ovh: invalid service name %q", serviceName)
	}
	return NewWithEndpoint(target.api, target.token, serviceName, creds, &http.Client{Timeout: 60 * time.Second}), nil
}

// NewWithEndpoint lets tests point the driver at a fake API.
func NewWithEndpoint(endpoint, tokenURL, serviceName string, creds Credentials, client *http.Client) *Driver {
	return &Driver{client: client, endpoint: strings.TrimRight(endpoint, "/"), tokenURL: tokenURL, service: serviceName, creds: creds, now: time.Now}
}

func (d *Driver) States(ctx context.Context, ids []string) (map[string]driver.State, error) {
	statuses, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	states := make(map[string]driver.State, len(ids))
	for _, id := range ids {
		states[id] = driver.Missing
		if status, found := statuses[id]; found {
			states[id] = stateOf(status)
		}
	}
	return states, nil
}

func stateOf(status string) driver.State {
	switch status {
	case "ACTIVE":
		return driver.Running
	case "SHUTOFF", "STOPPED", "SHELVED", "SHELVED_OFFLOADED":
		return driver.Stopped
	case "DELETED", "DELETING", "SOFT_DELETED":
		return driver.Missing
	default: // BUILD, SHELVING, UNSHELVING, ERROR, UNKNOWN...
		return driver.Changing
	}
}

// Start wakes each machine according to its current status. A machine already
// running or in transition needs nothing; one that no longer exists is an error.
func (d *Driver) Start(ctx context.Context, ids []string) error {
	statuses, err := d.list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		status, found := statuses[id]
		switch {
		case !found || stateOf(status) == driver.Missing:
			errs = append(errs, fmt.Errorf("ovh start %s: instance not found", id))
		case status == "SHELVED" || status == "SHELVED_OFFLOADED":
			errs = append(errs, d.act(ctx, id, "unshelve"))
		case status == "SHUTOFF" || status == "STOPPED":
			errs = append(errs, d.act(ctx, id, "start"))
		}
	}
	return errors.Join(errs...)
}

// Stop shelves each machine: on OVH a stopped instance keeps being billed, a
// shelved one does not. Every machine is tried and every error returned.
func (d *Driver) Stop(ctx context.Context, ids []string) error {
	var errs []error
	for _, id := range ids {
		errs = append(errs, d.act(ctx, id, "shelve"))
	}
	return errors.Join(errs...)
}

func (d *Driver) instances() string {
	return "/cloud/project/" + url.PathEscape(d.service) + "/instance"
}

// list returns the status of every instance of the project.
func (d *Driver) list(ctx context.Context) (map[string]string, error) {
	var out []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := d.call(ctx, http.MethodGet, "list", d.instances(), &out); err != nil {
		return nil, err
	}
	statuses := make(map[string]string, len(out))
	for _, in := range out {
		statuses[in.ID] = in.Status
	}
	return statuses, nil
}

func (d *Driver) act(ctx context.Context, id, action string) error {
	return d.call(ctx, http.MethodPost, action+" "+id, d.instances()+"/"+url.PathEscape(id)+"/"+action, nil)
}

type apiError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

func (d *Driver) call(ctx context.Context, method, action, path string, out any) error {
	token, err := d.bearer(ctx, "")
	if err != nil {
		return err
	}
	status, raw, err := d.send(ctx, method, path, token)
	if err == nil && status == http.StatusUnauthorized {
		// Refused before its expiry: ask for a new token, once.
		if token, err = d.bearer(ctx, token); err != nil {
			return err
		}
		status, raw, err = d.send(ctx, method, path, token)
	}
	if err != nil {
		return fmt.Errorf("ovh %s: %w", action, err)
	}
	if status < 200 || status > 299 {
		var e apiError
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return fmt.Errorf("ovh %s: HTTP %d, %s %s", action, status, e.Class, e.Message)
		}
		return fmt.Errorf("ovh %s: HTTP %d", action, status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ovh %s: %w", action, err)
	}
	return nil
}

func (d *Driver) send(ctx context.Context, method, path, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.endpoint+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, err
}

// bearer returns the cached token, or a new one when it is close to expiry or
// is the one the API just refused. Errors only ever name the OAuth2 error code,
// so that the answer of the token endpoint cannot carry a secret into the logs.
func (d *Driver) bearer(ctx context.Context, refused string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.token != "" && d.token != refused && d.now().Before(d.expiry) {
		return d.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {d.creds.ClientID},
		"client_secret": {d.creds.ClientSecret},
		"scope":         {"all"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ovh token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("ovh token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Code != "" {
			return "", fmt.Errorf("ovh token: HTTP %d, %s", resp.StatusCode, e.Code)
		}
		return "", fmt.Errorf("ovh token: HTTP %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
		return "", errors.New("ovh token: no access token in the answer")
	}
	d.token = out.AccessToken
	d.expiry = d.now().Add(time.Duration(out.ExpiresIn)*time.Second - tokenMargin)
	return d.token, nil
}
