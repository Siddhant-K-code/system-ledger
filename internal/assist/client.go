package assist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type runner struct {
	transport http.RoundTripper
	endpoint  string
	timeout   time.Duration
	lookupEnv func(string) (string, bool)
}

// Run prepares an exact, nonsecret request preview or sends one request with
// explicit remote consent. Dry runs never look up credentials or use a network.
// Provider endpoints cannot be configured by callers.
func Run(ctx context.Context, options Options, question string, pack Pack) (Result, error) {
	return (runner{}).run(ctx, options, question, pack)
}

func (r runner) run(ctx context.Context, options Options, question string, pack Pack) (Result, error) {
	preview, err := prepare(options, question, pack)
	if err != nil {
		return Result{}, err
	}
	if options.DryRun {
		return Result{Preview: &preview}, nil
	}
	if ctx == nil {
		return Result{}, errors.New("assistance requires a request context; no request was sent")
	}
	if ctx.Err() != nil {
		return Result{}, contextError(ctx.Err())
	}
	pack = normalizedPack(pack)
	envName := "OPENAI_API_KEY"
	if options.Provider == "anthropic" {
		envName = "ANTHROPIC_API_KEY"
	}
	lookup := r.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	key, exists := lookup(envName)
	if !exists || strings.TrimSpace(key) == "" {
		return Result{}, errors.New("set " + envName + " in the environment before permitting remote assistance; no request was sent")
	}
	for _, c := range key {
		if c <= ' ' || c > '~' {
			return Result{}, errors.New(envName + " is not a valid API key; no request was sent")
		}
	}
	if len(key) > 4096 {
		return Result{}, errors.New(envName + " is too long; no request was sent")
	}
	timeout := r.timeout
	if timeout <= 0 || timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := preview.Endpoint
	if r.endpoint != "" {
		endpoint = r.endpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(preview.Body))
	if err != nil {
		return Result{}, errors.New("could not construct assistance request; no request was sent")
	}
	// Prevent transport-level replay of a POST after a dropped connection.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if options.Provider == "openai" {
		request.Header.Set("Authorization", "Bearer "+key)
	} else {
		request.Header.Set("x-api-key", key)
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	transport := r.transport
	if transport == nil {
		transport = &http.Transport{
			DialContext:            (&net.Dialer{Timeout: MaxTimeout}).DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  MaxTimeout,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 32 * 1024,
		}
	}
	client := &http.Client{
		Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return Result{}, contextError(ctx.Err())
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return Result{}, contextError(context.DeadlineExceeded)
		}
		return Result{}, errors.New("assistance connection failed; check connectivity and TLS settings; no retry was attempted")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, statusError(response.StatusCode)
	}
	if response.ContentLength > MaxResponseBytes {
		return Result{}, errors.New("provider response exceeds 1 MiB; no suggestion was accepted")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, contextError(ctx.Err())
		}
		return Result{}, errors.New("could not read provider response; no retry was attempted")
	}
	if ctx.Err() != nil {
		return Result{}, contextError(ctx.Err())
	}
	if len(body) > MaxResponseBytes {
		return Result{}, errors.New("provider response exceeds 1 MiB; no suggestion was accepted")
	}
	text, usage, err := parseResponse(options.Provider, body)
	if err != nil {
		return Result{}, err
	}
	if err := validateOutputCredentials(body, text, key); err != nil {
		return Result{}, err
	}
	proposal, err := parseProposal(text, pack, options, usage)
	if err != nil {
		return Result{}, err
	}
	return Result{Proposal: &proposal}, nil
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("assistance request timed out; no retry was attempted: %w", context.DeadlineExceeded)
	}
	return fmt.Errorf("assistance request was canceled; no retry was attempted: %w", context.Canceled)
}

func statusError(status int) error {
	switch {
	case status >= 300 && status < 400:
		return errors.New("provider redirect refused; only the official provider endpoint is allowed")
	case status == http.StatusUnauthorized:
		return errors.New("provider authentication failed (401); check the selected provider API key environment variable")
	case status == http.StatusForbidden:
		return errors.New("provider denied access (403); check account and model permissions")
	case status == http.StatusTooManyRequests:
		return errors.New("provider rate limit or quota exceeded (429); check quota or try again later; no retry was attempted")
	case status >= 500 && status <= 599:
		return errors.New("provider service failed (5xx); try again later; no retry was attempted")
	default:
		return errors.New("provider rejected the request; check the configured model and structured-output support; no retry was attempted")
	}
}
