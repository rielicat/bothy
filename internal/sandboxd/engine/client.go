// Package engine is a minimal Docker Engine API client over a unix socket:
// the dozen endpoints sandboxd needs, without moby/client and its
// containerd/OCI/otel dependencies (SPEC D11).
package engine

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
	"sync"
	"time"
)

const (
	// MaxAPI is the newest API version this client speaks; newer engines
	// accept older versions, so requests are pinned to it.
	MaxAPI = "1.47"
	// MinAPI is the oldest version with volume subpaths (Docker 26).
	MinAPI = "1.45"
)

var (
	ErrNotFound = errors.New("engine: not found")
	ErrConflict = errors.New("engine: conflict")
)

// APIError is a non-2xx engine response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("engine: %d %s", e.Status, e.Message) }

func (e *APIError) Is(target error) bool {
	return (target == ErrNotFound && e.Status == http.StatusNotFound) ||
		(target == ErrConflict && e.Status == http.StatusConflict)
}

type Client struct {
	sock string
	hc   *http.Client

	mu      sync.Mutex
	version string
}

// New accepts "unix:///path/to/docker.sock" or a bare absolute path.
func New(endpoint string) (*Client, error) {
	sock := strings.TrimPrefix(endpoint, "unix://")
	if !strings.HasPrefix(sock, "/") {
		return nil, fmt.Errorf("engine: endpoint must be unix:///<absolute path>, got %q", endpoint)
	}
	c := &Client{sock: sock}
	c.hc = &http.Client{Transport: &http.Transport{
		DialContext:         c.dial,
		DisableCompression:  true,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 0,
	}}
	return c, nil
}

func (c *Client) Socket() string { return c.sock }

func (c *Client) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", c.sock)
}

// Negotiate reads the engine's API version from /_ping and pins requests to
// min(engine, MaxAPI). It returns the engine's own version.
func (c *Client) Negotiate(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Status: resp.StatusCode, Message: "ping failed"}
	}
	server := resp.Header.Get("Api-Version")
	if server == "" {
		return "", errors.New("engine: /_ping did not report an API version")
	}
	v := server
	if CompareVersions(v, MaxAPI) > 0 {
		v = MaxAPI
	}
	c.mu.Lock()
	c.version = v
	c.mu.Unlock()
	return server, nil
}

// APIVersion is the negotiated version, "" before Negotiate.
func (c *Client) APIVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

func (c *Client) url(path string, q url.Values) string {
	u := "http://docker"
	if v := c.APIVersion(); v != "" {
		u += "/v" + v
	}
	u += path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, q), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return readAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v, err
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.do(ctx, http.MethodGet, "/info", nil, nil, &i)
	return i, err
}

// ImageInspect reports whether ref exists locally (ErrNotFound otherwise).
func (c *Client) ImageInspect(ctx context.Context, ref string) error {
	return c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, nil)
}

// ImagePull pulls "name@sha256:<hex>" and waits for the progress stream to
// end; errors inside the stream are reported.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok {
		return fmt.Errorf("engine: refusing to pull %q: not pinned by digest", ref)
	}
	q := url.Values{"fromImage": {name}, "tag": {digest}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/images/create", q), nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readAPIError(resp)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("engine: pull %s: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("engine: pull %s: %s", ref, msg.Error)
		}
	}
}

func (c *Client) ContainerCreate(ctx context.Context, name string, cfg *ContainerConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, cfg, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

func (c *Client) ContainerKill(ctx context.Context, id, signal string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/kill", url.Values{"signal": {signal}}, nil, nil)
}

// ContainerRemove force-removes a container; a missing one is not an error.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	// 409 "removal already in progress" (AutoRemove racing us) is done too.
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && strings.Contains(ae.Message, "in progress") {
		return nil
	}
	return err
}

// ContainerList lists containers (running or not) carrying every label.
func (c *Client) ContainerList(ctx context.Context, labels []string) ([]Container, error) {
	f, err := json.Marshal(map[string][]string{"label": labels})
	if err != nil {
		return nil, err
	}
	var out []Container
	err = c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}}, nil, &out)
	return out, err
}

func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerJSON, error) {
	var out ContainerJSON
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ExecCreate(ctx context.Context, id string, cfg ExecConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil, cfg, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) ExecInspect(ctx context.Context, id string) (ExecInspect, error) {
	var out ExecInspect
	err := c.do(ctx, http.MethodGet, "/exec/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out, err
}

// ExecStart starts an exec and hijacks the connection: the returned stream
// carries stdin to the process and the multiplexed stdout/stderr frames back
// (see Demux). Cancelling ctx closes the stream.
func (c *Client) ExecStart(ctx context.Context, id string) (ExecStream, error) {
	conn, err := c.dial(ctx, "unix", c.sock)
	if err != nil {
		return nil, err
	}
	body := `{"Detach":false,"Tty":false}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/exec/"+url.PathEscape(id)+"/start", nil), strings.NewReader(body))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	if err := req.Write(conn); err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		err := readAPIError(resp)
		resp.Body.Close()
		stop()
		conn.Close()
		return nil, err
	}
	return &Stream{conn: conn, r: br, stop: stop}, nil
}

// Stream is a hijacked exec connection.
type Stream struct {
	conn net.Conn
	r    *bufio.Reader
	stop func() bool
	once sync.Once
}

func (s *Stream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *Stream) Write(p []byte) (int, error) { return s.conn.Write(p) }

// CloseWrite signals EOF on the process's stdin.
func (s *Stream) CloseWrite() error {
	if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (s *Stream) Close() error {
	var err error
	s.once.Do(func() {
		s.stop()
		err = s.conn.Close()
	})
	return err
}

// CompareVersions compares dotted API versions numerically ("1.9" < "1.45").
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
