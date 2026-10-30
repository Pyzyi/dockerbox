// Package dockerapi is a minimal client for the Docker Engine API over a unix
// socket. It implements only the calls Dockerbox needs (see ADR 0004).
package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIError is returned for non-2xx responses from the engine.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker api: %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is a 404 from the engine.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports whether err is a 409 from the engine.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// IsNotModified reports whether err is a 304 (e.g. container already started).
func IsNotModified(err error) bool { return statusIs(err, http.StatusNotModified) }

func statusIs(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// Client talks to the Docker Engine. The zero value is not usable; use New.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the engine listening on the given unix socket.
func New(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    8,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{socket: socket, http: &http.Client{Transport: tr}}
}

// Paths are unversioned so the engine uses its own default API version; every
// field we use exists since API 1.41 (Docker 20.10).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.raw(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) raw(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return resp, nil
}

func decodeError(resp *http.Response) error {
	var msg struct {
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &msg) != nil || msg.Message == "" {
		msg.Message = strings.TrimSpace(string(data))
	}
	if msg.Message == "" {
		msg.Message = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Message: msg.Message}
}

func filtersQuery(labels []string) url.Values {
	q := url.Values{}
	if len(labels) > 0 {
		f, _ := json.Marshal(map[string][]string{"label": labels})
		q.Set("filters", string(f))
	}
	return q
}

// Ping checks that the engine is reachable.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

// Version describes the engine.
type Version struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
}

// Version returns the engine version.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var out Version
	if err := c.do(ctx, http.MethodGet, "/version", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Info is the subset of GET /info Dockerbox uses.
type Info struct {
	NCPU int `json:"NCPU"`
}

// Info returns host information as seen by the engine.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var out Info
	if err := c.do(ctx, http.MethodGet, "/info", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ContainerList lists containers (including stopped ones) matching all labels.
func (c *Client) ContainerList(ctx context.Context, labels []string) ([]ContainerSummary, error) {
	q := filtersQuery(labels)
	q.Set("all", "1")
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

// ContainerCreate creates a container and returns its ID.
func (c *Client) ContainerCreate(ctx context.Context, name string, spec *ContainerSpec) (string, error) {
	q := url.Values{"name": {name}}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", q, spec, &out)
	return out.ID, err
}

// ContainerStart starts a container. Starting a running container is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

// ContainerStop stops a container, killing it after timeout seconds.
func (c *Client) ContainerStop(ctx context.Context, id string, timeout int) error {
	q := url.Values{"t": {strconv.Itoa(timeout)}}
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

// ContainerRestart restarts a container.
func (c *Client) ContainerRestart(ctx context.Context, id string, timeout int) error {
	q := url.Values{"t": {strconv.Itoa(timeout)}}
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/restart", q, nil, nil)
}

// ContainerRemove force-removes a container. Named volumes are kept.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	q := url.Values{"force": {"1"}}
	err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ContainerRename renames a container.
func (c *Client) ContainerRename(ctx context.Context, id, name string) error {
	q := url.Values{"name": {name}}
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", q, nil, nil)
}

// ContainerStats returns a single stats sample without waiting for a second one.
func (c *Client) ContainerStats(ctx context.Context, id string) (*Stats, error) {
	q := url.Values{"stream": {"0"}, "one-shot": {"1"}}
	var out Stats
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/stats", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImageExists reports whether an image reference is present locally.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// ImagePull pulls an image reference ("repo:tag" or "repo@digest").
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	q := url.Values{"fromImage": {ref}}
	if !strings.Contains(ref, "@") && !hasTag(ref) {
		q.Set("tag", "latest")
	}
	resp, err := c.raw(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The engine streams JSON progress messages; errors arrive in-band.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return &APIError{Status: http.StatusBadGateway, Message: msg.Error}
		}
	}
}

func hasTag(ref string) bool {
	slash := strings.LastIndex(ref, "/")
	return strings.Contains(ref[slash+1:], ":")
}

// NetworkCreate creates a network and returns its ID.
func (c *Client) NetworkCreate(ctx context.Context, spec *NetworkSpec) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/networks/create", nil, spec, &out)
	return out.ID, err
}

// NetworkInspect returns details of a network.
func (c *Client) NetworkInspect(ctx context.Context, id string) (*NetworkJSON, error) {
	var out NetworkJSON
	if err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NetworkList lists networks matching all labels.
func (c *Client) NetworkList(ctx context.Context, labels []string) ([]NetworkJSON, error) {
	var out []NetworkJSON
	err := c.do(ctx, http.MethodGet, "/networks", filtersQuery(labels), nil, &out)
	return out, err
}

// NetworkConnect attaches a container to a network with optional DNS aliases.
func (c *Client) NetworkConnect(ctx context.Context, network, container string, aliases []string) error {
	body := map[string]any{
		"Container":      container,
		"EndpointConfig": map[string]any{"Aliases": aliases},
	}
	return c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, body, nil)
}

// NetworkDisconnect detaches a container from a network.
func (c *Client) NetworkDisconnect(ctx context.Context, network, container string) error {
	body := map[string]any{"Container": container, "Force": true}
	return c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/disconnect", nil, body, nil)
}

// NetworkRemove deletes a network. Missing networks are not an error.
func (c *Client) NetworkRemove(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// VolumeInspect returns details of a volume.
func (c *Client) VolumeInspect(ctx context.Context, name string) (*VolumeJSON, error) {
	var out VolumeJSON
	if err := c.do(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VolumeRemove deletes a volume. Missing volumes are not an error.
func (c *Client) VolumeRemove(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ExecCreate prepares a command inside a running container and returns the exec ID.
func (c *Client) ExecCreate(ctx context.Context, container string, spec *ExecSpec) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec", nil, spec, &out)
	return out.ID, err
}

// ExecResize changes the TTY size of an exec session.
func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	q := url.Values{"h": {strconv.Itoa(rows)}, "w": {strconv.Itoa(cols)}}
	return c.do(ctx, http.MethodPost, "/exec/"+url.PathEscape(execID)+"/resize", q, nil, nil)
}

// ExecAttach starts an exec session with a TTY and returns the hijacked raw
// stream. Reads must go through the returned reader, which may already hold
// buffered output; writes go to the connection.
func (c *Client) ExecAttach(ctx context.Context, execID string) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, nil, err
	}
	body := []byte(`{"Detach":false,"Tty":true}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/exec/"+url.PathEscape(execID)+"/start", bytes.NewReader(body))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	// A stalled daemon must not pin this goroutine forever during the handshake.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		defer conn.Close()
		return nil, nil, decodeError(resp)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, nil
}
