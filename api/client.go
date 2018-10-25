package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/perlin-network/graph/wire"
	"github.com/perlin-network/wavelet/events"
	"github.com/perlin-network/wavelet/params"
	"github.com/perlin-network/wavelet/security"
	"github.com/pkg/errors"
)

// Client represents a Perlin Ledger client.
type Client struct {
	Config       ClientConfig
	SessionToken string
	KeyPair      *security.KeyPair
}

// ClientConfig represents a Perlin Ledger client config.
type ClientConfig struct {
	APIHost    string
	APIPort    uint
	PrivateKey string
	UseHTTPS   bool
}

type RequestOptions struct {
	ContentType  string
	SendRawBytes bool
}

// NewClient creates a new Perlin Ledger client from a config.
func NewClient(config ClientConfig) (*Client, error) {
	keys, err := security.FromPrivateKey(security.SignaturePolicy, config.PrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "Missing authentication key")
	}

	return &Client{
		Config:  config,
		KeyPair: keys,
	}, nil
}

// Init will initialize a client.
func (c *Client) Init() error {
	millis := time.Now().Unix() * 1000
	authStr := fmt.Sprintf("%s%d", sessionInitSigningPrefix, millis)
	sig := security.Sign(c.KeyPair.PrivateKey, []byte(authStr))

	creds := credentials{
		PublicKey:  hex.EncodeToString(c.KeyPair.PublicKey),
		TimeMillis: millis,
		Sig:        hex.EncodeToString(sig),
	}

	resp := SessionResponse{}

	err := c.Request(RouteSessionInit, &creds, &resp, nil)
	if err != nil {
		return err
	}
	c.SessionToken = resp.Token
	return nil
}

// EstablishWS will create a websocket connection.
func (c *Client) EstablishWS(path string) (*websocket.Conn, error) {
	prot := "ws"
	if c.Config.UseHTTPS {
		prot = "wss"
	}

	url := fmt.Sprintf("%s://%s:%d%s", prot, c.Config.APIHost, c.Config.APIPort, path)

	header := make(http.Header)
	header.Add(HeaderSessionToken, c.SessionToken)

	dialer := &websocket.Dialer{}
	conn, _, err := dialer.Dial(url, header)
	return conn, err
}

// Request will make a request to a given path, with a given body and return result in out.
func (c *Client) Request(path string, body, out interface{}, opts *RequestOptions) error {
	prot := "http"
	if c.Config.UseHTTPS {
		prot = "https"
	}
	u, err := url.Parse(fmt.Sprintf("%s://%s:%d%s", prot, c.Config.APIHost, c.Config.APIPort, path))
	if err != nil {
		return err
	}
	req := &http.Request{
		Method: "POST",
		URL:    u,
		Header: map[string][]string{
			HeaderSessionToken: []string{c.SessionToken},
			HeaderUserAgent:    []string{userAgent()},
		},
	}

	if opts != nil && len(opts.ContentType) > 0 {
		req.Header["Content-type"] = []string{opts.ContentType}
	}

	if body != nil {
		if opts != nil && opts.SendRawBytes {
			req.Body = ioutil.NopCloser(bytes.NewReader(body.([]byte)))
		} else {
			rawBody, err := json.Marshal(body)
			if err != nil {
				return err
			}
			req.Body = ioutil.NopCloser(bytes.NewReader(rawBody))
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Errorf("got an error code %v: %v", resp.Status, string(data))
	}

	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// PollAcceptedTransactions polls for accepted transactions.
func (c *Client) PollAcceptedTransactions(stop <-chan struct{}) (<-chan wire.Transaction, error) {
	return c.pollTransactions("accepted", stop)
}

// PollAppliedTransactions polls for applied transactions.
func (c *Client) PollAppliedTransactions(stop <-chan struct{}) (<-chan wire.Transaction, error) {
	return c.pollTransactions("applied", stop)
}

// PollAccountUpdates polls for updates to accounts within the ledger.
func (c *Client) PollAccountUpdates(stop <-chan struct{}) (<-chan events.AccountUpdateEvent, error) {
	if stop == nil {
		stop = make(chan struct{})
	}

	ws, err := c.EstablishWS(RouteAccountPoll)
	if err != nil {
		return nil, err
	}

	evChan := make(chan events.AccountUpdateEvent)

	go func() {
		defer close(evChan)

		for {
			var ev events.AccountUpdateEvent

			if err = ws.ReadJSON(&ev); err != nil {
				return
			}
			select {
			case <-stop:
				return
			case evChan <- ev:
			}
		}
	}()

	return evChan, nil
}

// pollTransactions starts polling events from a websocket connection.
func (c *Client) pollTransactions(event string, stop <-chan struct{}) (<-chan wire.Transaction, error) {
	if stop == nil {
		stop = make(chan struct{})
	}

	ws, err := c.EstablishWS(RouteTransactionPoll + "?event=" + event)
	if err != nil {
		return nil, err
	}

	evChan := make(chan wire.Transaction)

	go func() {
		defer close(evChan)

		for {
			var ev wire.Transaction

			if err = ws.ReadJSON(&ev); err != nil {
				return
			}
			select {
			case <-stop:
				return
			case evChan <- ev:
			}
		}
	}()

	return evChan, nil

}

func (c *Client) SendTransaction(tag string, payload []byte) error {
	return c.Request(RouteTransactionSend, SendTransaction{
		Tag:     tag,
		Payload: payload,
	}, nil, nil)
}

func (c *Client) ListTransaction(offset uint64, limit uint64) (transactions []*wire.Transaction, err error) {
	err = c.Request(RouteTransactionList, Paginate{
		Offset: &offset,
		Limit:  &limit,
	}, &transactions, nil)

	return
}

func (c *Client) RecentTransactions() (transactions []*wire.Transaction, err error) {
	err = c.Request(RouteTransactionList, nil, &transactions, nil)
	return
}

// StatsReset will reset a client statistics.
func (c *Client) StatsReset(res interface{}) error {
	return c.Request(RouteStatsReset, nil, res, nil)
}

func (c *Client) LoadAccount(id string) (map[string][]byte, error) {
	var ret map[string][]byte
	if err := c.Request(RouteAccountLoad, id, &ret, nil); err != nil {
		return nil, err
	}

	return ret, nil
}

func (c *Client) ServerVersion() (sv *ServerVersion, err error) {
	err = c.Request(RouteServerVersion, nil, &sv, nil)
	return
}

func (c *Client) LedgerState() (*LedgerState, error) {
	var ret LedgerState
	if err := c.Request(RouteLedgerState, nil, &ret, nil); err != nil {
		return nil, err
	}
	return &ret, nil
}

func (c *Client) SendContract(filename string) (string, error) {
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)

	// this step is very important
	fileWriter, err := bodyWriter.CreateFormFile(UploadFormField, filename)
	if err != nil {
		return "", errors.Wrap(err, "error writing to buffer")
	}

	// open file handle
	fh, err := os.Open(filename)
	if err != nil {
		return "", errors.Wrap(err, "error opening file")
	}
	defer fh.Close()

	// iocopy
	if _, err = io.Copy(fileWriter, fh); err != nil {
		return "", errors.Wrap(err, "error copy the file")
	}

	opts := &RequestOptions{
		ContentType:  bodyWriter.FormDataContentType(),
		SendRawBytes: true,
	}
	bodyWriter.Close()

	var result struct {
		ContractID string `json:"contract_id"`
	}
	if err := c.Request(RouteContractSend, bodyBuf.Bytes(), &result, opts); err != nil {
		return "", err
	}
	return result.ContractID, nil
}

// userAgent is a short summary of the client type making the connection
func userAgent() string {
	return fmt.Sprintf("wctl/%s-%s (%s)", params.Version, params.GitCommit, params.OSArch)
}
