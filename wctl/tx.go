package wctl

import (
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"

	"github.com/perlin-network/noise/edwards25519"
	"github.com/valyala/fastjson"
)

var (
	_ UnmarshalableJSON = (*TxResponse)(nil)
	_ UnmarshalableJSON = (*Transaction)(nil)
	_ UnmarshalableJSON = (*TransactionList)(nil)
	_ MarshalableJSON   = (*TxRequest)(nil)
)

var (
	// ErrInsufficientPerls is returned when you don't have enough PERLs.
	ErrInsufficientPerls = errors.New("Insufficient PERLs")
)

// ListTransactions calls the /tx endpoint of the API to list all transactions.
// The arguments are optional, zero values would default them.
func (c *Client) ListTransactions(senderID *[32]byte, creatorID *[32]byte, offset uint64, limit uint64) ([]Transaction, error) {
	vals := url.Values{}

	if senderID != nil {
		vals.Set("sender", string(senderID[:]))
	}

	if creatorID != nil {
		vals.Set("creator", string(creatorID[:]))
	}

	if offset != 0 {
		vals.Set("offset", strconv.FormatUint(offset, 10))
	}

	if limit != 0 {
		vals.Set("limit", strconv.FormatUint(limit, 10))
	}

	path := RouteTxList + "?" + vals.Encode()

	var res TransactionList
	if err := c.RequestJSON(path, ReqGet, nil, &res); err != nil {
		return nil, err
	}

	return res, nil
}

// GetTransaction calls the /tx endpoint to query a single transaction.
func (c *Client) GetTransaction(txID [32]byte) (*Transaction, error) {
	path := RouteTxList + "/" + string(txID[:])

	var res Transaction
	if err := c.RequestJSON(path, ReqGet, nil, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// SendTransaction calls the /tx/send endpoint to send a raw payload.
// Payloads are best crafted with wavelet.Transfer.
func (c *Client) sendTransaction(tag byte, payload []byte) (*TxResponse, error) {
	var res TxResponse

	var nonce [8]byte // TODO(kenta): nonce

	signature := edwards25519.Sign(
		c.PrivateKey,
		append(nonce[:], append([]byte{tag}, payload...)...),
	)

	req := TxRequest{
		Sender:    hex.EncodeToString(c.PublicKey[:]),
		Tag:       tag,
		Payload:   hex.EncodeToString(payload),
		Signature: hex.EncodeToString(signature[:]),
	}

	if err := c.RequestJSON(RouteTxSend, ReqPost, &req, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// SendTransfer sends a wavelet.Transfer instead of a Payload.
func (c *Client) sendTransfer(tag byte, transfer Marshalable) (*TxResponse, error) {
	return c.sendTransaction(tag, transfer.Marshal())
}

type Transaction struct {
	ID string `json:"id"`

	Sender  string `json:"sender"`
	Creator string `json:"creator"`

	Parents []string `json:"parents"`

	Timestamp uint64 `json:"timestamp"`

	Tag     byte   `json:"tag"`
	Payload []byte `json:"payload"`

	AccountsMerkleRoot string `json:"accounts_root"`

	SenderSignature  string `json:"sender_signature"`
	CreatorSignature string `json:"creator_signature"`

	Depth uint64 `json:"depth"`
}

func (t *Transaction) UnmarshalJSON(b []byte) error {
	var parser fastjson.Parser

	v, err := parser.ParseBytes(b)
	if err != nil {
		return err
	}

	t.ParseJSON(v)

	return nil
}

func (t *Transaction) ParseJSON(v *fastjson.Value) {
	t.ID = string(v.GetStringBytes("id"))
	t.Sender = string(v.GetStringBytes("sender"))
	t.Creator = string(v.GetStringBytes("creator"))

	parentsValue := v.GetArray("parents")
	for _, parent := range parentsValue {
		t.Parents = append(t.Parents, parent.String())
	}

	t.Timestamp = v.GetUint64("timestamp")
	t.Tag = byte(v.GetUint("tag"))
	t.Payload = v.GetStringBytes("payload")
	t.AccountsMerkleRoot = string(v.GetStringBytes("accounts_root"))
	t.SenderSignature = string(v.GetStringBytes("sender_signature"))
	t.CreatorSignature = string(v.GetStringBytes("creator_signature"))
	t.Depth = v.GetUint64("depth")
}

type TransactionList []Transaction

func (t *TransactionList) UnmarshalJSON(b []byte) error {
	var parser fastjson.Parser

	v, err := parser.ParseBytes(b)
	if err != nil {
		return err
	}

	a, err := v.Array()
	if err != nil {
		return err
	}

	var list []Transaction

	for _, v := range a {
		tx := &Transaction{}
		tx.ParseJSON(v)

		list = append(list, *tx)
	}

	*t = list

	return nil
}

type TxRequest struct {
	Sender    string `json:"sender"`
	Tag       byte   `json:"tag"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func (s *TxRequest) MarshalJSON() ([]byte, error) {
	var arena fastjson.Arena
	o := arena.NewObject()

	o.Set("sender", arena.NewString(s.Sender))
	o.Set("tag", arena.NewNumberInt(int(s.Tag)))
	o.Set("payload", arena.NewString(s.Payload))
	o.Set("signature", arena.NewString(s.Signature))

	return o.MarshalTo(nil), nil
}

type TxResponse struct {
	ID       string   `json:"tx_id"`
	Parents  []string `json:"parent_ids"`
	Critical bool     `json:"is_critical"`
}

func (s *TxResponse) UnmarshalJSON(b []byte) error {
	var parser fastjson.Parser

	v, err := parser.ParseBytes(b)
	if err != nil {
		return err
	}

	s.ID = string(v.GetStringBytes("tx_id"))

	parentsValue := v.GetArray("parent_ids")
	for _, parent := range parentsValue {
		s.Parents = append(s.Parents, parent.String())
	}

	s.Critical = v.GetBool("is_critical")

	return nil
}