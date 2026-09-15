package hosts

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

var errUnknownHost = shared.New("HOST_UNKNOWN", shared.ErrNotFound, "no address known for this host")

type Signer interface {
	Sign(payload []byte) []byte
}

type Directory map[vo.Participant]string

func (d Directory) baseURL(participant vo.Participant) (string, error) {
	url, found := d[participant]
	if !found {
		return "", errUnknownHost
	}
	return url, nil
}

type Client struct {
	http      *http.Client
	directory Directory
	signer    Signer
	clock     ports.Clock
	timeout   time.Duration
}

func New(client *http.Client, directory Directory, signer Signer, clock ports.Clock, timeout time.Duration) *Client {
	return &Client{http: client, directory: directory, signer: signer, clock: clock, timeout: timeout}
}

func (c *Client) call(ctx context.Context, participant vo.Participant, method, path string, requestID vo.RequestID, body, out any) error {
	base, err := c.directory.baseURL(participant)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	request, err := c.request(ctx, participant, method, base, path, requestID, payload)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return shared.New("HOST_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer response.Body.Close()

	var envelope contract.Envelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, fmt.Sprintf("host answered %d with no envelope", response.StatusCode))
	}
	if !envelope.OK {
		return toError(response.StatusCode, envelope.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c *Client) request(ctx context.Context, participant vo.Participant, method, base, path string, requestID vo.RequestID, payload []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	timestamp := c.clock.Now().UTC().Format(time.RFC3339)
	signature := c.signer.Sign(contract.SigningPayload(string(participant), method, path, request.URL.RawQuery, timestamp, string(requestID), payload))

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contract.HeaderTimestamp, timestamp)
	request.Header.Set(contract.HeaderRequestID, string(requestID))
	request.Header.Set(contract.HeaderSignature, hex.EncodeToString(signature))
	return request, nil
}

func (c *Client) stream(ctx context.Context, participant vo.Participant, method, path string, requestID vo.RequestID, body any, out io.Writer) error {
	base, err := c.directory.baseURL(participant)
	if err != nil {
		return err
	}

	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	request, err := c.request(ctx, participant, method, base, path, requestID, payload)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return shared.New("HOST_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		var envelope contract.Envelope
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			return toError(response.StatusCode, nil)
		}
		return toError(response.StatusCode, envelope.Error)
	}

	_, err = io.Copy(out, response.Body)
	return err
}
