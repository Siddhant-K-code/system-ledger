// Package assist sends bounded, typed evidence to an explicitly selected provider.
// It does not discover credentials, read source files, or execute model output.
package assist

import (
	"encoding/json"
	"time"
)

const (
	MaxAssets              = 32
	MaxRelationships       = 64
	MaxEvidence            = 256
	MaxGaps                = 64
	MaxOwners              = 32
	MaxAttributes          = 16
	MaxStringBytes         = 4096
	MaxIDBytes             = 256
	MaxModelBytes          = 256
	MaxQuestionBytes       = 8 * 1024
	MaxRequestBytes        = 256 * 1024
	MaxResponseBytes       = 1024 * 1024
	MaxOutputBytes         = 64 * 1024
	MaxSuggestions         = 32
	MaxAssumptions         = 32
	MaxUnansweredQuestions = 32
	DefaultMaxOutputTokens = 2048
	MaxOutputTokens        = 4096
	MaxTimeout             = 30 * time.Second
	UnverifiedLabel        = "unverified proposal"
)

type Pack struct {
	SelectedAssetID string         `json:"selected_asset_id"`
	Assets          []Asset        `json:"assets"`
	Evidence        []Evidence     `json:"evidence"`
	Relationships   []Relationship `json:"relationships"`
	Gaps            []Gap          `json:"gaps"`
}

type Owner struct {
	Name string `json:"name"`
	Team string `json:"team"`
}

type Asset struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Attributes  map[string]string `json:"attributes"`
	Owners      []Owner           `json:"owners"`
	EvidenceIDs []string          `json:"evidence_ids"`
}

type Evidence struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	SHA256  string `json:"sha256"`
	Locator string `json:"locator"`
}

type Relationship struct {
	ID          string   `json:"id"`
	FromID      string   `json:"from_id"`
	ToID        string   `json:"to_id"`
	Type        string   `json:"type"`
	Origin      string   `json:"origin"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Gap struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Locator string `json:"locator"`
}

type Options struct {
	Provider        string
	Model           string
	DryRun          bool
	AllowRemote     bool
	MaxOutputTokens int
}

type Preview struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Endpoint string          `json:"endpoint"`
	Body     json.RawMessage `json:"body"`
}

type Result struct {
	Preview  *Preview  `json:"preview,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
}

type Suggestion struct {
	Text        string   `json:"text"`
	AssetIDs    []string `json:"asset_ids"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

type Proposal struct {
	Label               string       `json:"label"`
	Provider            string       `json:"provider"`
	Model               string       `json:"model"`
	Suggestions         []Suggestion `json:"suggestions"`
	Assumptions         []string     `json:"assumptions"`
	UnansweredQuestions []string     `json:"unanswered_questions"`
	AffectedOwners      []Owner      `json:"affected_owners"`
	Usage               *Usage       `json:"usage,omitempty"`
}
