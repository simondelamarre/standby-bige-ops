// Package outscale drives Outscale VMs through the OUTSCALE API: ReadVms,
// StartVms and StopVms. It never force-stops a VM and calls nothing else.
package outscale

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/driver"
)

var regionRe = regexp.MustCompile(`^[a-z]+(-[a-z]+)*-[0-9]+$`)

// Credentials is an access key pair. It is never logged.
type Credentials struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// LoadCredentials reads a JSON key pair from a file only its owner can read.
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
		return Credentials{}, fmt.Errorf("%s: not a JSON key pair", path)
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return Credentials{}, fmt.Errorf("%s: access_key and secret_key are required", path)
	}
	return c, nil
}

type Driver struct {
	client   *http.Client
	endpoint string
	region   string
	creds    Credentials
	now      func() time.Time
}

// New targets the public API of region, for instance eu-west-2.
func New(region string, creds Credentials) (*Driver, error) {
	if !regionRe.MatchString(region) {
		return nil, fmt.Errorf("outscale: invalid region %q", region)
	}
	return NewWithEndpoint("https://api."+region+".outscale.com/api/v1", region, creds, &http.Client{Timeout: 60 * time.Second}), nil
}

// NewWithEndpoint lets tests point the driver at a fake API.
func NewWithEndpoint(endpoint, region string, creds Credentials, client *http.Client) *Driver {
	return &Driver{client: client, endpoint: strings.TrimRight(endpoint, "/"), region: region, creds: creds, now: time.Now}
}

func (d *Driver) States(ctx context.Context, ids []string) (map[string]driver.State, error) {
	var out struct {
		Vms []struct {
			VmId  string `json:"VmId"`
			State string `json:"State"`
		} `json:"Vms"`
	}
	if err := d.call(ctx, "ReadVms", map[string]any{"Filters": map[string]any{"VmIds": ids}}, &out); err != nil {
		return nil, err
	}
	states := make(map[string]driver.State, len(ids))
	for _, id := range ids {
		states[id] = driver.Missing
	}
	for _, vm := range out.Vms {
		if _, wanted := states[vm.VmId]; !wanted {
			continue
		}
		switch vm.State {
		case "running":
			states[vm.VmId] = driver.Running
		case "stopped":
			states[vm.VmId] = driver.Stopped
		case "terminated", "shutting-down":
			states[vm.VmId] = driver.Missing
		default: // pending, stopping, quarantine
			states[vm.VmId] = driver.Changing
		}
	}
	return states, nil
}

func (d *Driver) Start(ctx context.Context, ids []string) error {
	return d.call(ctx, "StartVms", map[string]any{"VmIds": ids}, nil)
}

// Stop asks for a graceful shutdown; ForceStop is always false.
func (d *Driver) Stop(ctx context.Context, ids []string) error {
	return d.call(ctx, "StopVms", map[string]any{"VmIds": ids, "ForceStop": false}, nil)
}

type apiError struct {
	Errors []struct {
		Code    string `json:"Code"`
		Type    string `json:"Type"`
		Details string `json:"Details"`
	} `json:"Errors"`
}

func (d *Driver) call(ctx context.Context, action string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint+"/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	sign(req, body, d.creds, d.region, d.now())
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("outscale %s: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("outscale %s: %w", action, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		if json.Unmarshal(raw, &e) == nil && len(e.Errors) > 0 {
			first := e.Errors[0]
			return fmt.Errorf("outscale %s: HTTP %d, %s %s %s", action, resp.StatusCode, first.Code, first.Type, first.Details)
		}
		return fmt.Errorf("outscale %s: HTTP %d", action, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("outscale %s: %w", action, err)
	}
	return nil
}

// sign adds the OSC4-HMAC-SHA256 headers (AWS SigV4 adapted by OUTSCALE).
func sign(req *http.Request, body []byte, c Credentials, region string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonicalHeaders := "content-type:application/json\nhost:" + req.URL.Host + "\nx-osc-date:" + amzDate + "\n"
	const signedHeaders = "content-type;host;x-osc-date"
	canonicalRequest := strings.Join([]string{http.MethodPost, path, "", canonicalHeaders, signedHeaders, hexSHA256(body)}, "\n")
	scope := day + "/" + region + "/api/osc4_request"
	stringToSign := strings.Join([]string{"OSC4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest))}, "\n")
	key := hmacSHA256([]byte("OSC4"+c.SecretKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "api")
	key = hmacSHA256(key, "osc4_request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Osc-Date", amzDate)
	req.Header.Set("Authorization", "OSC4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
