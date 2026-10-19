package regruapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const Endpoint = "https://api.reg.ru/api/regru2/"

type Kind int

const (
	Read Kind = iota
	Mutate
)

const maxBodyBytes = 16 << 20

var errRedirectRefused = errors.New("redirect refused")

type Client struct {
	cr          Credentials
	lim         *Limiter
	baseURL     string
	httpClient  *http.Client
	timeout     time.Duration
	maxAttempts int
	clock       Clock
}

type Option func(*Client)

func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

func WithRequestTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

func WithMaxAttempts(n int) Option { return func(c *Client) { c.maxAttempts = n } }

func WithClock(clk Clock) Option { return func(c *Client) { c.clock = clk } }

func New(cr Credentials, lim *Limiter, opts ...Option) *Client {
	c := &Client{
		cr:          cr,
		lim:         lim,
		baseURL:     Endpoint,
		timeout:     30 * time.Second,
		maxAttempts: 3,
		clock:       SystemClock{},
	}
	for _, o := range opts {
		o(c)
	}
	if c.maxAttempts < 1 {
		c.maxAttempts = 1
	}
	var hc http.Client
	if c.httpClient != nil {
		hc = *c.httpClient
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirectRefused }
	c.httpClient = &hc
	return c
}

type envelope struct {
	Result      string          `json:"result"`
	Answer      json.RawMessage `json:"answer"`
	ErrorCode   string          `json:"error_code"`
	ErrorText   string          `json:"error_text"`
	ErrorParams json.RawMessage `json:"error_params"`
}

func (c *Client) Call(ctx context.Context, kind Kind, method string, input map[string]any) (json.RawMessage, error) {
	if input == nil {
		input = map[string]any{}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode input for %s: %w", method, err)
	}
	form := url.Values{}
	form.Set("username", c.cr.Username)
	form.Set("password", c.cr.Password)
	form.Set("input_format", "json")
	form.Set("input_data", string(data))
	encoded := form.Encode()
	target := strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(method, "/")

	attempts := 1
	if kind == Read {
		attempts = c.maxAttempts
	}
	var last error
	for i := 1; i <= attempts; i++ {
		if i > 1 {
			backoff := time.Duration(1<<(i-2)) * time.Second
			if err := c.clock.Sleep(ctx, backoff); err != nil {
				return nil, err
			}
		}
		if err := c.lim.Wait(ctx); err != nil {
			return nil, err
		}
		ans, retry, err := c.once(ctx, kind, method, target, encoded)
		if err == nil {
			return ans, nil
		}
		last = err
		if !retry {
			break
		}
	}
	return nil, last
}

func (c *Client) once(ctx context.Context, kind Kind, method, target, encoded string) (json.RawMessage, bool, error) {
	fail := func(err error, retry bool) (json.RawMessage, bool, error) {
		err = c.cr.redactErr(err)
		if kind == Mutate {
			return nil, false, &UnknownOutcomeError{Method: method, Err: err}
		}
		return nil, retry, &TransportError{Method: method, Err: err}
	}

	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, target, strings.NewReader(encoded))
	if err != nil {
		return nil, false, &TransportError{Method: method, Err: c.cr.redactErr(err)}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			if kind == Mutate {
				return nil, false, &UnknownOutcomeError{Method: method, Err: c.cr.redactErr(ctx.Err())}
			}
			return nil, false, ctx.Err()
		}
		return fail(err, !errors.Is(err, errRedirectRefused))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if ctx.Err() != nil && kind == Read {
			return nil, false, ctx.Err()
		}
		return fail(err, true)
	}
	if resp.StatusCode >= 500 {
		return fail(fmt.Errorf("http status %d", resp.StatusCode), true)
	}
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("http status %d", resp.StatusCode), false)
	}
	var env envelope
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&env); err != nil {
		return fail(errors.New("response is not valid json"), true)
	}
	switch env.Result {
	case "success":
		return env.Answer, false, nil
	case "error":
		ae := &APIError{
			Code:   c.cr.redact(env.ErrorCode),
			Text:   c.cr.redact(env.ErrorText),
			Params: c.cr.redactParams(env.ErrorParams),
		}
		return nil, ae.Code == "SERVICE_UNAVAILABLE" && kind == Read, ae
	default:
		return fail(errors.New("unexpected result in response"), false)
	}
}
