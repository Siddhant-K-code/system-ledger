package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRejectMalformedModelsWithoutEchoOrCredentialAccess(t *testing.T) {
	for _, model := range []string{
		"model\n" + syntheticKey, "model\t" + syntheticKey, "model\r" + syntheticKey,
		"model\x1b" + syntheticKey, "model\x7f" + syntheticKey, "model\u0085" + syntheticKey,
		"model\u202e" + syntheticKey, "model\u200b" + syntheticKey, " model ", "\x00" + syntheticKey,
	} {
		for _, provider := range []string{"openai", "anthropic"} {
			options := testOptions(provider)
			options.Model = model
			runner := runner{lookupEnv: func(string) (string, bool) {
				t.Fatal("malformed model accessed credentials")
				return "", false
			}}
			for _, dry := range []bool{false, true} {
				options.DryRun, options.AllowRemote = dry, !dry
				result, err := runner.run(context.Background(), options, "What changes?", testPack())
				if err == nil || strings.Contains(err.Error(), syntheticKey) || strings.ContainsAny(err.Error(), "\x1b\n\r") ||
					result.Preview != nil || result.Proposal != nil {
					t.Fatalf("malformed model accepted or echoed: %v", err)
				}
			}
		}
	}
}

func TestObviousCredentialMarkersBlockedBeforePreviewOrRemote(t *testing.T) {
	markers := []string{
		"sk-proj-" + strings.Repeat("X", 24),
		"sk-ant-api03-" + strings.Repeat("X", 40),
		"sk-" + strings.Repeat("X", 24),
		"ghp_" + strings.Repeat("X", 36),
		"github_pat_" + strings.Repeat("X", 32),
		"AKIA" + strings.Repeat("X", 16),
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"OPENAI_API_KEY=synthetic-assigned-value",
		`"ANTHROPIC_API_KEY": "synthetic-assigned-value"`,
		"Bearer " + strings.Repeat("X", 32),
	}
	for _, provider := range []string{"openai", "anthropic"} {
		for index, marker := range markers {
			t.Run(fmt.Sprintf("%s/%d", provider, index), func(t *testing.T) {
				for _, field := range []string{"question", "model", "asset", "attribute", "owner", "source", "locator", "gap"} {
					options := testOptions(provider)
					question := "What changes?"
					pack := testPack()
					switch field {
					case "question":
						question += " " + marker
					case "model":
						options.Model = marker
					case "asset":
						pack.Assets[0].Name = marker
					case "attribute":
						pack.Assets[0].Attributes["path"] = marker
					case "owner":
						pack.Assets[0].Owners[0].Name = marker
					case "source":
						pack.Evidence[0].Source = marker
					case "locator":
						pack.Evidence[0].Locator = marker
					case "gap":
						pack.Gaps[0].Message = marker
					}
					runner := runner{lookupEnv: func(string) (string, bool) {
						t.Fatal("credential-bearing input accessed environment")
						return "", false
					}, transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						t.Fatal("credential-bearing input made a request")
						return nil, nil
					})}
					for _, dry := range []bool{false, true} {
						options.DryRun, options.AllowRemote = dry, !dry
						result, err := runner.run(context.Background(), options, question, pack)
						if err == nil || strings.Contains(err.Error(), marker) || result.Preview != nil || result.Proposal != nil {
							t.Fatalf("credential marker in %s accepted or echoed: %v", field, err)
						}
					}
				}
			})
		}
	}
}

func TestCredentialDiscussionWithoutValuesAllowed(t *testing.T) {
	for _, question := range []string{
		"Should OPENAI_API_KEY and ANTHROPIC_API_KEY be configured only in the environment?",
		"Should the API key be rotated?",
		"Which service owns asia-distribution-routing?",
	} {
		options := testOptions("openai")
		options.DryRun, options.AllowRemote = true, false
		result, err := Run(context.Background(), options, question, testPack())
		if err != nil || result.Preview == nil {
			t.Fatalf("non-credential discussion rejected: %v", err)
		}
	}
}

func TestActualSelectedAPIKeyRejectedInProviderOutput(t *testing.T) {
	setSyntheticKeys(t)
	var escaped strings.Builder
	for _, r := range syntheticKey {
		fmt.Fprintf(&escaped, `\u%04x`, r)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		for _, location := range []string{"text", "escaped text", "assumption", "question", "metadata", "escaped metadata"} {
			t.Run(provider+"/"+location, func(t *testing.T) {
				text := validProposalJSON()
				switch location {
				case "text":
					text = strings.Replace(text, "Consider checking the route contract.", syntheticKey, 1)
				case "escaped text":
					text = strings.Replace(text, "Consider checking the route contract.", escaped.String(), 1)
				case "assumption":
					text = strings.Replace(text, "Static evidence may be incomplete.", syntheticKey, 1)
				case "question":
					text = strings.Replace(text, "Does a runtime query use this table?", syntheticKey, 1)
				}
				body := responseJSON(t, provider, text)
				if strings.Contains(location, "metadata") {
					response := decodeObject(t, body)
					response["metadata"] = map[string]any{"key": syntheticKey}
					var err error
					body, err = json.Marshal(response)
					if err != nil {
						t.Fatal(err)
					}
					if location == "escaped metadata" {
						body = bytes.ReplaceAll(body, []byte(syntheticKey), []byte(escaped.String()))
					}
				}
				calls := 0
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
				})
				result, err := (runner{transport: transport}).run(context.Background(), testOptions(provider), "What changes?", testPack())
				if err == nil || !strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), syntheticKey) ||
					strings.Contains(err.Error(), escaped.String()) || result.Proposal != nil || result.Preview != nil || calls != 1 {
					t.Fatalf("credential-bearing provider output accepted or echoed: %v (calls %d)", err, calls)
				}
			})
		}
	}
}
