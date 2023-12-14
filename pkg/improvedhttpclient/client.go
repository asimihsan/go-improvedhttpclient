package improvedhttpclient

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/asimihsan/adaptiveretry/go/pkg/retry"
	"github.com/benbjohnson/clock"
	"github.com/rs/zerolog"
	"go.uber.org/ratelimit"
)

var (
	defaultHTTPClient = http.Client{
		Timeout: time.Second * 10,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     time.Second * 300,
			DisableCompression:  false,
			DialContext: (&net.Dialer{
				Timeout:   time.Second * 10,
				KeepAlive: time.Second * 3,
			}).DialContext,
			ResponseHeaderTimeout: time.Second * 10,
			ExpectContinueTimeout: time.Second * 1,
		},
	}
)

type TransportWrapper func(http.RoundTripper) http.RoundTripper

type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type HTTPClient struct {
	innerClient *http.Client
	rateLimiter ratelimit.Limiter
}

type HTTPClientConfig struct {
	rateLimitRequestsPerSecond int
	clock                      Clock
	wireLoggingEnabled         bool
	transportWrappers          []TransportWrapper
	logger                     *zerolog.Logger
}

type HTTPClientOption func(*HTTPClientConfig)

func WithWireLogging(logger *zerolog.Logger) HTTPClientOption {
	return func(c *HTTPClientConfig) {
		c.logger = logger
		c.wireLoggingEnabled = true
	}
}

func WithRateLimitRequestsPerSecond(rps int) HTTPClientOption {
	return func(c *HTTPClientConfig) {
		c.rateLimitRequestsPerSecond = rps
	}
}

func WithClock(clock Clock) HTTPClientOption {
	return func(c *HTTPClientConfig) {
		c.clock = clock
	}
}

func WithTransportWrapper(wrapper TransportWrapper) HTTPClientOption {
	return func(c *HTTPClientConfig) {
		c.transportWrappers = append(c.transportWrappers, wrapper)
	}
}

func NewHTTPClient(opts ...HTTPClientOption) (*HTTPClient, error) {
	config := &HTTPClientConfig{
		clock:                      clock.New(),
		rateLimitRequestsPerSecond: 1,
	}
	for _, opt := range opts {
		opt(config)
	}

	innerClient := defaultHTTPClient

	if config.wireLoggingEnabled {
		innerClient.Transport = NewLoggingTransport(innerClient.Transport, config.logger)
	}

	transport := innerClient.Transport
	for _, wrapper := range config.transportWrappers {
		transport = wrapper(transport)
	}

	innerClient.Transport = transport

	client := &HTTPClient{
		innerClient: &innerClient,
		rateLimiter: ratelimit.New(
			config.rateLimitRequestsPerSecond,
			ratelimit.WithClock(config.clock),
		),
	}

	return client, nil
}

func (client *HTTPClient) Do(ctx context.Context, req *http.Request) (resp *http.Response, err error) {
	retryer := retry.NewRetryer(retry.NewDefaultConfig())
	_ = retryer.Do(ctx, func(ctx context.Context) error {
		client.rateLimiter.Take()              // This will block until a request can be made
		resp, err = client.innerClient.Do(req) //nolint:bodyclose
		return err
	})
	return
}
