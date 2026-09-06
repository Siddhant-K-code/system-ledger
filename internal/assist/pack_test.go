package assist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestInvalidPackFailsBeforeCredentialsOrNetwork(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Pack)
	}{
		{"no assets", func(p *Pack) { p.Assets = nil }},
		{"too many assets", func(p *Pack) { p.Assets = make([]Asset, MaxAssets+1) }},
		{"too much evidence", func(p *Pack) { p.Evidence = make([]Evidence, MaxEvidence+1) }},
		{"too many relationships", func(p *Pack) { p.Relationships = make([]Relationship, MaxRelationships+1) }},
		{"too many gaps", func(p *Pack) { p.Gaps = make([]Gap, MaxGaps+1) }},
		{"selected unknown", func(p *Pack) { p.SelectedAssetID = "outside" }},
		{"asset duplicate", func(p *Pack) { p.Assets[1].ID = p.Assets[0].ID }},
		{"evidence duplicate", func(p *Pack) { p.Evidence[1].ID = p.Evidence[0].ID }},
		{"cross kind duplicate", func(p *Pack) { p.Evidence[0].ID = p.Assets[0].ID }},
		{"empty id", func(p *Pack) { p.Assets[0].ID = "" }},
		{"oversized id", func(p *Pack) { p.Assets[0].ID = strings.Repeat("a", MaxIDBytes+1) }},
		{"empty kind", func(p *Pack) { p.Assets[0].Kind = "" }},
		{"empty asset name", func(p *Pack) { p.Assets[0].Name = "" }},
		{"oversized name", func(p *Pack) { p.Assets[0].Name = strings.Repeat("a", MaxStringBytes+1) }},
		{"invalid utf8", func(p *Pack) { p.Assets[0].Name = string([]byte{0xff}) }},
		{"attribute excerpt", func(p *Pack) { p.Assets[0].Attributes["excerpt"] = "source code" }},
		{"attribute description", func(p *Pack) { p.Assets[0].Attributes["description"] = "source prose" }},
		{"attribute oversized", func(p *Pack) { p.Assets[0].Attributes["path"] = strings.Repeat("a", MaxStringBytes+1) }},
		{"too many attributes", func(p *Pack) {
			for i := 0; i < MaxAttributes+1; i++ {
				p.Assets[0].Attributes[fmt.Sprint(i)] = "value"
			}
		}},
		{"too many owners", func(p *Pack) { p.Assets[0].Owners = make([]Owner, MaxOwners+1) }},
		{"invalid owner", func(p *Pack) { p.Assets[0].Owners[0].Name = "" }},
		{"invalid team", func(p *Pack) { p.Assets[0].Owners[0].Team = strings.Repeat("a", MaxStringBytes+1) }},
		{"unknown asset evidence", func(p *Pack) { p.Assets[0].EvidenceIDs = []string{"outside"} }},
		{"duplicate asset evidence", func(p *Pack) { p.Assets[0].EvidenceIDs = []string{"evidence:a", "evidence:a"} }},
		{"invalid evidence source", func(p *Pack) { p.Evidence[0].Source = "" }},
		{"invalid evidence locator", func(p *Pack) { p.Evidence[0].Locator = "" }},
		{"short digest", func(p *Pack) { p.Evidence[0].SHA256 = "aa" }},
		{"nonhex digest", func(p *Pack) { p.Evidence[0].SHA256 = strings.Repeat("z", 64) }},
		{"unknown relationship endpoint", func(p *Pack) { p.Relationships[0].FromID = "outside" }},
		{"unknown relationship target", func(p *Pack) { p.Relationships[0].ToID = "outside" }},
		{"missing relationship type", func(p *Pack) { p.Relationships[0].Type = "" }},
		{"missing relationship origin", func(p *Pack) { p.Relationships[0].Origin = "" }},
		{"unknown relationship evidence", func(p *Pack) { p.Relationships[0].EvidenceIDs = []string{"outside"} }},
		{"missing gap code", func(p *Pack) { p.Gaps[0].Code = "" }},
		{"missing gap message", func(p *Pack) { p.Gaps[0].Message = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pack := testPack()
			test.change(&pack)
			runner := runner{
				lookupEnv: func(string) (string, bool) {
					t.Fatal("invalid pack accessed credentials")
					return "", false
				},
				transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid pack attempted networking")
					return nil, errors.New("not allowed")
				}),
			}
			for _, provider := range []string{"openai", "anthropic"} {
				options := testOptions(provider)
				if _, err := runner.run(context.Background(), options, "What changes?", pack); err == nil {
					t.Fatal("invalid pack accepted for remote request")
				}
				options.AllowRemote, options.DryRun = false, true
				if _, err := runner.run(context.Background(), options, "What changes?", pack); err == nil {
					t.Fatal("invalid pack accepted for preview")
				}
			}
		})
	}
}

func TestRequestByteLimitBeforeCredentials(t *testing.T) {
	pack := testPack()
	pack.Assets[0].Owners = make([]Owner, MaxOwners)
	for i := range pack.Assets[0].Owners {
		// The nested user-data string is JSON encoded again in each provider body.
		pack.Assets[0].Owners[i] = Owner{Name: strings.Repeat(`"`, MaxStringBytes), Team: strings.Repeat(`"`, MaxStringBytes)}
	}
	if err := validatePack(pack); err != nil {
		t.Fatalf("test needs a structurally valid pack: %v", err)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		runner := runner{lookupEnv: func(string) (string, bool) {
			t.Fatal("oversized request accessed credentials")
			return "", false
		}}
		_, err := runner.run(context.Background(), testOptions(provider), "What changes?", pack)
		if err == nil || !strings.Contains(err.Error(), "exceeds 256 KiB") {
			t.Fatalf("oversized %s request accepted: %v", provider, err)
		}
	}
}

func TestMinimalPackNormalizesWithoutMutatingCaller(t *testing.T) {
	pack := Pack{SelectedAssetID: "a", Assets: []Asset{{ID: "a", Kind: "function", Name: "Example"}}}
	options := testOptions("openai")
	options.AllowRemote, options.DryRun = false, true
	result, err := Run(context.Background(), options, "What changes?", pack)
	if err != nil || result.Preview == nil {
		t.Fatalf("minimal pack rejected: %v", err)
	}
	if pack.Evidence != nil || pack.Assets[0].Attributes != nil || pack.Assets[0].Owners != nil {
		t.Fatal("preview modified caller's pack")
	}
	input := decodeObject(t, []byte(decodeObject(t, result.Preview.Body)["input"].(string)))
	gotPack := input["pack"].(map[string]any)
	for _, name := range []string{"evidence", "relationships", "gaps"} {
		if gotPack[name] == nil {
			t.Errorf("pack %s array was serialized as null", name)
		}
	}
}
