package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/execx"
)

// API is the slice of the Proxmox API careen uses directly: the QEMU guest
// agent, to know a new VM is up and read its SSH host key without
// trusting the network. Hardware changes go through OpenTofu instead.
type API struct {
	Endpoint string // https://host:8006/
	Token    string // user@realm!id=uuid
	Client   *http.Client
	// OnWait, when set, is told how long WaitHostKey has left and why the
	// agent hasn't answered yet, every so often.
	OnWait func(left time.Duration, last error)
}

// NewAPI talks to endpoint, accepting Proxmox's self-signed certificate
// (as the OpenTofu provider config does).
func NewAPI(endpoint, token string) *API {
	return &API{Endpoint: endpoint, Token: token, Client: &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}}
}

// StatusError is a non-2xx answer.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("Proxmox API: %d %s", e.Code, execx.Tail(e.Body, 2))
}

func (a *API) do(ctx context.Context, method, path string, out any) error {
	u := strings.TrimSuffix(a.Endpoint, "/") + "/api2/json" + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+a.Token)
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return &StatusError{Code: resp.StatusCode, Body: string(body)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Reboot asks Proxmox to reboot vmid (through the guest agent or ACPI).
func (a *API) Reboot(ctx context.Context, node string, vmid int) error {
	return a.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/reboot", url.PathEscape(node), vmid), nil)
}

// HostKeyPath is the key careen reads and replaces on a new VM.
const HostKeyPath = "/etc/ssh/ssh_host_ed25519_key.pub"

// waitNotice is how often WaitHostKey says it is still waiting.
const waitNotice = 15 * time.Second

// WaitHostKey waits until vmid's guest agent answers and the VM has an
// ed25519 host key, and returns it as "ssh-ed25519 AAAA...". Answers that
// mean "not yet" (the agent isn't running, the key isn't written) are
// retried every poll until timeout; refusals (401/403) end it at once.
func (a *API) WaitHostKey(ctx context.Context, node string, vmid int, poll, timeout time.Duration) (string, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := fmt.Sprintf("/nodes/%s/qemu/%d/agent", url.PathEscape(node), vmid)
	var last error
	deadline, _ := ctx.Deadline()
	var told time.Time
	for {
		key, err := a.readHostKey(ctx, base)
		if err == nil {
			return key, nil
		}
		if se, ok := err.(*StatusError); ok && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
			return "", fmt.Errorf("reading VM %d's host key: %w (does the API token have VM.GuestAgent.Audit and VM.GuestAgent.FileRead?)", vmid, err)
		}
		last = err
		if a.OnWait != nil && time.Since(told) >= waitNotice {
			told = time.Now()
			a.OnWait(time.Until(deadline), err)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("VM %d's guest agent didn't report a host key within %s: %v", vmid, timeout, last)
		case <-time.After(poll):
		}
	}
}

func (a *API) readHostKey(ctx context.Context, base string) (string, error) {
	if err := a.do(ctx, http.MethodPost, base+"/ping", nil); err != nil {
		return "", err
	}
	var res struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, base+"/file-read?file="+url.QueryEscape(HostKeyPath), &res); err != nil {
		return "", err
	}
	f := strings.Fields(res.Data.Content)
	if len(f) < 2 || f[0] != "ssh-ed25519" {
		return "", fmt.Errorf("no ed25519 key in %s yet", HostKeyPath)
	}
	return f[0] + " " + f[1], nil
}
