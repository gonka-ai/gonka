package public

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type ChatRequest struct {
	Body              []byte
	ForwardPath       string
	ForwardBody       []byte
	Request           *http.Request
	OpenAiRequest     OpenAiRequest
	AuthKey           string // signature signing inference request
	Seed              string
	InferenceId       string
	RequesterAddress  string // address of participant, who signed inference request
	TransferAddress   string
	Timestamp         int64  // timestamp of the request
	TransferSignature string // signature of the transfer address
	PromptHash        string
	SignBodyHash      string
}

type OpenAiRequest struct {
	Model               string    `json:"model"`
	Seed                int32     `json:"seed"`
	MaxTokens           int32     `json:"max_tokens"`
	MaxCompletionTokens int32     `json:"max_completion_tokens"`
	Messages            []Message `json:"messages"`
}

type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string           `json:"role"`
		Content *json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if strings.TrimSpace(raw.Role) == "" {
		return fmt.Errorf("message role must be a non-empty string")
	}
	m.Role = raw.Role
	if raw.Content == nil {
		return nil
	}

	if err := json.Unmarshal(*raw.Content, &m.Content); err != nil {
		return err
	}
	return nil
}

type MessageContent struct {
	Text  *string
	Parts []ContentPart
}

type ContentPart struct {
	raw  json.RawMessage
	Type string
	Text string
}

func (p *ContentPart) UnmarshalJSON(data []byte) error {
	p.raw = append(p.raw[:0], data...)
	var fields struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	p.Type = fields.Type
	p.Text = fields.Text
	return nil
}

func (p ContentPart) MarshalJSON() ([]byte, error) {
	if p.raw != nil {
		// Preserve original part payload so unknown OpenAI content-part fields
		// (e.g. image_url metadata and future part types) survive round-trips.
		return p.raw, nil
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}{Type: p.Type, Text: p.Text})
}

func (c *MessageContent) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		c.Text = nil
		c.Parts = nil
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = &text
		c.Parts = nil
		return nil
	}

	var parts []ContentPart
	if err := json.Unmarshal(data, &parts); err == nil {
		c.Text = nil
		c.Parts = parts
		return nil
	}

	return fmt.Errorf("message content must be a string or an array of typed content parts")
}

func (c MessageContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return json.Marshal(*c.Text)
	}
	if c.Parts != nil {
		return json.Marshal(c.Parts)
	}
	return []byte("null"), nil
}

func (c MessageContent) FlattenedText() (string, int) {
	if c.Text != nil {
		return *c.Text, 0
	}

	var b strings.Builder
	ignoredParts := 0
	for _, part := range c.Parts {
		if part.Type == "text" {
			b.WriteString(part.Text)
			continue
		}
		ignoredParts++
	}

	return b.String(), ignoredParts
}

func FlattenMessagesText(messages []Message) (string, int) {
	var b strings.Builder
	ignoredParts := 0
	for _, message := range messages {
		text, ignored := message.Content.FlattenedText()
		b.WriteString(text)
		b.WriteByte('\n')
		ignoredParts += ignored
	}
	return b.String(), ignoredParts
}

// ContentText keeps backward-compatible text extraction behavior.
func (m Message) ContentText() string {
	text, _ := m.Content.FlattenedText()
	return text
}

type ExecutorDestination struct {
	Url     string `json:"url"`
	Address string `json:"address"`
}

type PricingDto struct {
	Price  uint64          `json:"unit_of_compute_price"` // Legacy field for backward compatibility
	Models []ModelPriceDto `json:"models"`
	// Dynamic pricing information
	DynamicPricingEnabled bool `json:"dynamic_pricing_enabled"`
}

type ModelPriceDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"` // Legacy field for backward compatibility
	PricePerToken          uint64 `json:"price_per_token"`            // Current price (dynamic or legacy)
	// Model metrics information
	Utilization *float64 `json:"utilization,omitempty"` // Current utilization if available
	Capacity    *int64   `json:"capacity,omitempty"`    // Model capacity if available
}

type AccountDto struct {
	Pubkey  string `json:"pubkey"`
	Balance int64  `json:"balance"`
	Denom   string `json:"denom"`
}

// FinalizedBlock represents a finalized block with optional receipts
type BridgeBlock struct {
	BlockNumber  string          `json:"blockNumber"`
	OriginChain  string          `json:"originChain"`        // Name of the origin chain (e.g., "ethereum")
	ReceiptsRoot string          `json:"receiptsRoot"`       // Merkle root of receipts trie for transaction verification
	Receipts     []BridgeReceipt `json:"receipts,omitempty"` // Optional list of receipts
}
type BridgeReceipt struct {
	ContractAddress string `json:"contract"`     // Address of the smart contract on the origin chain
	OwnerAddress    string `json:"owner"`        // Address of the token owner on the origin chain
	OwnerPubKey     string `json:"publicKey"`    // Public key of the token owner on the origin chain
	Amount          string `json:"amount"`       // Amount of tokens to be bridged
	ReceiptIndex    string `json:"receiptIndex"` // Index of the transaction receipt in the block
}
