// loggingTransport.go
package improvedhttpclient

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"
)

type LoggingTransportInterface interface {
	RoundTrip(req *http.Request) (*http.Response, error)
}

type LoggingTransport struct {
	transport http.RoundTripper
	logger    *zerolog.Logger
}

func NewLoggingTransport(
	transport http.RoundTripper,
	logger *zerolog.Logger,
) *LoggingTransport {
	return &LoggingTransport{
		transport: transport,
		logger:    logger,
	}
}

func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	var err error

	// Check if req.Body is not nil before trying to read
	if req.Body != nil {
		// Save a copy of the request body
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}

		// Truncate and indicate if the body is larger than 1MB
		var logReqBody []byte
		if len(reqBody) > 1<<20 {
			logReqBody = append(reqBody[:1<<20], []byte("...(truncated)")...)
		} else {
			logReqBody = reqBody
		}

		req.Body = io.NopCloser(bytes.NewBuffer(reqBody))

		t.logger.Debug().
			Str("method", req.Method).
			Str("url", req.URL.String()).
			Str("body", string(logReqBody)).
			Msg("Sending request")
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		t.logger.Error().Err(err).Msg("Received error")
		return nil, err
	}

	var respBody []byte

	// Check if resp.Body is not nil before trying to read
	if resp.Body != nil {
		// Save a copy of the response body
		respBody, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		// Truncate and indicate if the body is larger than 1MB
		var logRespBody []byte
		if len(respBody) > 1<<20 {
			logRespBody = append(respBody[:1<<20], []byte("...(truncated)")...)
		} else {
			logRespBody = respBody
		}

		resp.Body = io.NopCloser(bytes.NewBuffer(respBody))

		t.logger.Debug().
			Str("status", resp.Status).
			Str("body", string(logRespBody)).
			Msg("Received response")
	}

	return resp, nil
}
