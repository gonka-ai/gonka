package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

func (c *Client) call(ctx context.Context, method, path string, query url.Values, body, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return resp.StatusCode, engineError(resp)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	return resp.StatusCode, nil
}

func (c *Client) stream(ctx context.Context, method, path string, query url.Values, body any) (io.ReadCloser, error) {
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		return nil, engineError(resp)
	}
	return resp.Body, nil
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	request, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	return resp, nil
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any) (*http.Request, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}

	target := "http://docker/" + c.cfg.APIVersion + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func (c *Client) attach(ctx context.Context, path string, body any) (net.Conn, *bufio.Reader, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.cfg.Socket)
	if err != nil {
		return nil, nil, err
	}

	request, err := c.request(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")

	if err := request.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		return nil, nil, engineError(resp)
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	return conn, reader, nil
}

func engineError(resp *http.Response) error {
	var answer struct {
		Message string `json:"message"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&answer)
	if answer.Message == "" {
		return fmt.Errorf("docker %s: %s", resp.Request.URL.Path, resp.Status)
	}
	return fmt.Errorf("docker %s: %s", resp.Status, answer.Message)
}
