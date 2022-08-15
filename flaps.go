package flaps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var NonceHeader = "fly-machine-lease-nonce"

type Client struct {
	orgSlug    string
	appName    string
	host       string
	authToken  string
	httpClient *http.Client
}

func New(host, authToken, orgSlug, appName string) (*Client, error) {
	return NewWithClient(host, authToken, orgSlug, appName, http.DefaultClient)
}

func NewWithClient(host, authToken, orgSlug, appName string, httpClient *http.Client) (*Client, error) {
	return &Client{
		appName:    appName,
		orgSlug:    orgSlug,
		host:       host,
		authToken:  authToken,
		httpClient: httpClient,
	}, nil
}

func (f *Client) CreateApp(ctx context.Context, name string, org string) (err error) {
	in := map[string]interface{}{
		"app_name": name,
		"org_slug": org,
	}

	err = f.sendRequest(ctx, http.MethodPost, "/apps", in, nil, nil)
	return
}

func (f *Client) Launch(ctx context.Context, builder LaunchMachineInput) (*Machine, error) {
	var endpoint string
	if builder.ID != "" {
		endpoint = fmt.Sprintf("/%s", builder.ID)
	}

	out := new(Machine)

	if err := f.sendRequest(ctx, http.MethodPost, endpoint, builder, out, nil); err != nil {
		return nil, fmt.Errorf("failed to launch VM: %w", err)
	}

	return out, nil
}

func (f *Client) Update(ctx context.Context, builder LaunchMachineInput, nonce string) (*Machine, error) {
	headers := make(map[string][]string)

	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	endpoint := fmt.Sprintf("/%s", builder.ID)

	out := new(Machine)

	if err := f.sendRequest(ctx, http.MethodPost, endpoint, builder, out, headers); err != nil {
		return nil, fmt.Errorf("failed to update VM %s: %w", builder.ID, err)
	}
	return out, nil
}

func (f *Client) Start(ctx context.Context, machineID string) (*MachineStartResponse, error) {
	startEndpoint := fmt.Sprintf("/%s/start", machineID)

	out := new(MachineStartResponse)

	if err := f.sendRequest(ctx, http.MethodPost, startEndpoint, nil, out, nil); err != nil {
		return nil, fmt.Errorf("failed to start VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) Wait(ctx context.Context, machine *Machine, state string) (err error) {
	waitEndpoint := fmt.Sprintf("/%s/wait", machine.ID)

	version := machine.InstanceID

	if machine.Version != "" {
		version = machine.Version
	}
	if version != "" {
		waitEndpoint += fmt.Sprintf("?instance_id=%s&timeout=30", version)
	} else {
		waitEndpoint += "?timeout=30"
	}

	if state == "" {
		state = "started"
	}

	waitEndpoint += fmt.Sprintf("&state=%s", state)

	if err := f.sendRequest(ctx, http.MethodGet, waitEndpoint, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to wait for VM %s in %s state: %w", machine.ID, state, err)
	}
	return
}

func (f *Client) Stop(ctx context.Context, machine StopMachineInput) (err error) {
	stopEndpoint := fmt.Sprintf("/%s/stop", machine.ID)

	if err := f.sendRequest(ctx, http.MethodPost, stopEndpoint, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to stop VM %s: %w", machine.ID, err)
	}
	return
}

func (f *Client) Get(ctx context.Context, machineID string) (*Machine, error) {
	getEndpoint := ""

	if machineID != "" {
		getEndpoint = fmt.Sprintf("/%s", machineID)
	}

	out := new(Machine)

	err := f.sendRequest(ctx, http.MethodGet, getEndpoint, nil, out, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) List(ctx context.Context, state string) ([]*Machine, error) {
	getEndpoint := ""

	if state != "" {
		getEndpoint = fmt.Sprintf("?%s", state)
	}

	out := make([]*Machine, 0)

	err := f.sendRequest(ctx, http.MethodGet, getEndpoint, nil, &out, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	return out, nil
}

func (f *Client) Destroy(ctx context.Context, input RemoveMachineInput) (err error) {
	destroyEndpoint := fmt.Sprintf("/%s?kill=%t", input.ID, input.Kill)

	if err := f.sendRequest(ctx, http.MethodDelete, destroyEndpoint, nil, nil, nil); err != nil {
		return fmt.Errorf("failed to destroy VM %s: %w", input.ID, err)
	}

	return
}

func (f *Client) Kill(ctx context.Context, machineID string) (err error) {
	in := map[string]interface{}{
		"signal": 9,
	}
	err = f.sendRequest(ctx, http.MethodPost, fmt.Sprintf("/%s/signal", machineID), in, nil, nil)

	if err != nil {
		return fmt.Errorf("failed to kill VM %s: %w", machineID, err)
	}
	return
}

func (f *Client) GetLease(ctx context.Context, machineID string, ttl *int) (*MachineLease, error) {
	endpoint := fmt.Sprintf("/%s/lease", machineID)

	if ttl != nil {
		endpoint += fmt.Sprintf("?ttl=%d", *ttl)
	}

	out := new(MachineLease)

	err := f.sendRequest(ctx, http.MethodPost, endpoint, nil, out, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get lease on VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) ReleaseLease(ctx context.Context, machineID, nonce string) error {
	endpoint := fmt.Sprintf("/%s/lease", machineID)

	headers := make(map[string][]string)

	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	return f.sendRequest(ctx, http.MethodDelete, endpoint, nil, nil, headers)
}

func (f *Client) sendRequest(ctx context.Context, method, endpoint string, in, out interface{}, headers map[string][]string) error {
	req, err := f.NewRequest(ctx, method, endpoint, in, headers)
	if err != nil {
		return err
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return handleAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	}
	return nil
}

func (f *Client) NewRequest(ctx context.Context, method, path string, in interface{}, headers map[string][]string) (*http.Request, error) {
	var (
		body io.Reader
		host = f.host
	)

	if headers == nil {
		headers = make(map[string][]string)
	}

	targetEndpoint := fmt.Sprintf("http://[%s]:4280/v1/apps/%s/machines%s", host, f.appName, path)

	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		headers["Content-Type"] = []string{"application/json"}

		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetEndpoint, body)
	if err != nil {
		return nil, fmt.Errorf("could not create new request, %w", err)
	}
	req.Header = headers

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", f.authToken))

	return req, nil
}

func handleAPIError(resp *http.Response) error {
	switch resp.StatusCode / 100 {
	case 1, 3:
		return fmt.Errorf("API returned unexpected status, %d", resp.StatusCode)
	case 4, 5:
		apiErr := struct {
			Error   string `json:"error"`
			Message string `json:"message,omitempty"`
		}{}
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
			return fmt.Errorf("request returned non-2xx status, %d", resp.StatusCode)
		}
		if apiErr.Message != "" {
			return fmt.Errorf("%s", apiErr.Message)
		}
		return errors.New(apiErr.Error)
	default:
		return errors.New("something went terribly wrong")
	}
}
