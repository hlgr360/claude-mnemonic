package hooks

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes caps a worker response so a misbehaving peer cannot exhaust memory.
const maxResponseBytes = 32 << 20

// Minimal HTTP/1.0 client over a raw TCP socket. Linking net/http adds ~3 MB to
// every hook binary; hooks only speak plain JSON to the local worker. HTTP/1.0
// makes the server skip chunked encoding and close the connection, so the body
// is simply everything after the header block.
type httpResponse struct {
	Status     int
	StatusText string
	Body       []byte
}

func httpDo(ctx context.Context, timeout time.Duration, method string, port int, path string, body []byte) (*httpResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.0\r\nHost: %s\r\n", method, path, addr)
	if body != nil {
		fmt.Fprintf(&req, "Content-Type: application/json\r\nContent-Length: %d\r\n", len(body))
	}
	req.WriteString("\r\n")
	req.Write(body)
	if _, err := conn.Write(req.Bytes()); err != nil {
		return nil, err
	}

	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(conn), maxResponseBytes))
	if err != nil {
		return nil, err
	}
	return parseHTTPResponse(raw)
}

func parseHTTPResponse(raw []byte) (*httpResponse, error) {
	head, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		return nil, errors.New("malformed http response")
	}
	statusLine, _, _ := strings.Cut(string(head), "\r\n")
	_, rest, ok := strings.Cut(statusLine, " ")
	if !ok {
		return nil, fmt.Errorf("malformed status line %q", statusLine)
	}
	codeStr, _, _ := strings.Cut(rest, " ")
	code, err := strconv.Atoi(codeStr)
	if err != nil {
		return nil, fmt.Errorf("malformed status line %q", statusLine)
	}
	return &httpResponse{Status: code, StatusText: rest, Body: body}, nil
}

// GETBody fetches path from the worker within timeout and returns the raw body.
// Statuses of 400 and above are returned as errors.
func GETBody(ctx context.Context, timeout time.Duration, port int, path string) ([]byte, error) {
	resp, err := httpDo(ctx, timeout, "GET", port, path, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status >= 400 {
		return nil, fmt.Errorf("request failed: %s", resp.StatusText)
	}
	return resp.Body, nil
}
