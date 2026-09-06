package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func reasoningItem() map[string]any {
	return map[string]any{"id": "rs_synthetic", "type": "reasoning", "summary": []any{}}
}

func responseWithReasoning(t *testing.T, item map[string]any) map[string]any {
	t.Helper()
	response := decodeObject(t, responseJSON(t, "openai", validProposalJSON()))
	response["output"] = append([]any{item}, response["output"].([]any)...)
	return response
}

func marshalReasoningResponse(t *testing.T, response map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestOpenAIReasoningMetadataIgnored(t *testing.T) {
	setSyntheticKeys(t)
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"minimal", func(map[string]any) {}},
		{"completed", func(item map[string]any) { item["status"] = "completed" }},
		{"summary", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": "private-summary-marker"}}
		}},
		{"content", func(item map[string]any) {
			item["content"] = []any{map[string]any{"type": "reasoning_text", "text": "private-reasoning-marker"}}
		}},
		{"encrypted", func(item map[string]any) { item["encrypted_content"] = "private-encrypted-marker" }},
		{"null encrypted", func(item map[string]any) { item["encrypted_content"] = nil }},
		{"all metadata", func(item map[string]any) {
			item["status"] = "completed"
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": "private-summary-marker"}}
			item["content"] = []any{map[string]any{"type": "reasoning_text", "text": "private-reasoning-marker"}}
			item["encrypted_content"] = "private-encrypted-marker"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := reasoningItem()
			test.change(item)
			body := marshalReasoningResponse(t, responseWithReasoning(t, item))
			text, usage, err := parseResponse("openai", body)
			if err != nil || text != validProposalJSON() || usage == nil || *usage.InputTokens != 123 {
				t.Fatalf("reasoning metadata rejected or merged with proposal text: %v", err)
			}
			options := testOptions("openai")
			options.Model = "explicit-reasoning-model-no-allowlist"
			calls := 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				data, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				requestBody := decodeObject(t, data)
				if requestBody["model"] != options.Model || requestBody["store"] != false || requestBody["tools"] != nil {
					t.Fatal("reasoning support changed model, retention, or enabled tools")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			result, err := (runner{transport: transport}).run(context.Background(), options, "What changes?", testPack())
			if err != nil || calls != 1 || result.Proposal == nil || result.Proposal.Model != options.Model {
				t.Fatalf("reasoning model request failed: %v (calls %d)", err, calls)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-summary-marker", "private-reasoning-marker", "private-encrypted-marker", "rs_synthetic"} {
				if bytes.Contains(encoded, []byte(private)) {
					t.Fatal("private reasoning metadata escaped into result")
				}
			}
			if len(result.Proposal.Suggestions) != 1 || result.Proposal.Suggestions[0].Text != "Consider checking the route contract." {
				t.Fatal("reasoning metadata replaced or changed the validated proposal")
			}
		})
	}
}

func TestRejectInvalidOpenAIReasoningMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing id", func(item map[string]any) { delete(item, "id") }},
		{"empty id", func(item map[string]any) { item["id"] = "" }},
		{"numeric id", func(item map[string]any) { item["id"] = 1 }},
		{"missing summary", func(item map[string]any) { delete(item, "summary") }},
		{"null summary", func(item map[string]any) { item["summary"] = nil }},
		{"string summary", func(item map[string]any) { item["summary"] = "private-summary-marker" }},
		{"summary missing text", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "summary_text"}}
		}},
		{"summary null text", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": nil}}
		}},
		{"summary tool", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "function_call", "name": "shell"}}
		}},
		{"summary refusal", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "refusal", "text": "private-refusal-marker"}}
		}},
		{"summary output text", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "output_text", "text": validProposalJSON()}}
		}},
		{"summary unknown field", func(item map[string]any) {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": "private-summary-marker", "tool_calls": []any{}}}
		}},
		{"incomplete", func(item map[string]any) { item["status"] = "incomplete" }},
		{"in progress", func(item map[string]any) { item["status"] = "in_progress" }},
		{"unknown status", func(item map[string]any) { item["status"] = "unknown" }},
		{"null status", func(item map[string]any) { item["status"] = nil }},
		{"numeric status", func(item map[string]any) { item["status"] = 1 }},
		{"null content", func(item map[string]any) { item["content"] = nil }},
		{"content tool", func(item map[string]any) {
			item["content"] = []any{map[string]any{"type": "function_call", "name": "shell"}}
		}},
		{"content output text", func(item map[string]any) {
			item["content"] = []any{map[string]any{"type": "output_text", "text": validProposalJSON()}}
		}},
		{"content missing text", func(item map[string]any) {
			item["content"] = []any{map[string]any{"type": "reasoning_text"}}
		}},
		{"encrypted object", func(item map[string]any) { item["encrypted_content"] = map[string]any{} }},
		{"unknown field", func(item map[string]any) { item["unexpected"] = "private-unknown-marker" }},
		{"refusal", func(item map[string]any) { item["refusal"] = "private-refusal-marker" }},
		{"tool calls", func(item map[string]any) { item["tool_calls"] = []any{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := reasoningItem()
			test.change(item)
			body := marshalReasoningResponse(t, responseWithReasoning(t, item))
			if _, _, err := parseResponse("openai", body); err == nil {
				t.Fatal("invalid reasoning metadata accepted")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatal("reasoning metadata echoed in error")
			}
		})
	}
}

func TestOpenAIReasoningStillRequiresCompletedAssistantOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"reasoning only", func(response map[string]any) { response["output"] = []any{reasoningItem()} }},
		{"proposal in summary only", func(response map[string]any) {
			item := reasoningItem()
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": validProposalJSON()}}
			response["output"] = []any{item}
		}},
		{"incomplete response", func(response map[string]any) { response["status"] = "incomplete" }},
		{"incomplete assistant", func(response map[string]any) {
			response["output"].([]any)[1].(map[string]any)["status"] = "incomplete"
		}},
		{"assistant refusal", func(response map[string]any) {
			response["output"].([]any)[1].(map[string]any)["content"] = []any{map[string]any{"type": "refusal", "refusal": "No"}}
		}},
		{"tool item", func(response map[string]any) {
			response["output"] = append(response["output"].([]any), map[string]any{"type": "function_call", "name": "shell"})
		}},
		{"unknown item", func(response map[string]any) {
			response["output"] = append(response["output"].([]any), map[string]any{"type": "future_output_item"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := responseWithReasoning(t, reasoningItem())
			test.change(response)
			if _, _, err := parseResponse("openai", marshalReasoningResponse(t, response)); err == nil {
				t.Fatal("accepted unsupported output or reasoning without completed assistant text")
			}
		})
	}
}

func TestAPIKeyStillRejectedInIgnoredReasoning(t *testing.T) {
	setSyntheticKeys(t)
	for _, location := range []string{"summary", "content", "encrypted_content"} {
		t.Run(location, func(t *testing.T) {
			item := reasoningItem()
			switch location {
			case "summary":
				item["summary"] = []any{map[string]any{"type": "summary_text", "text": syntheticKey}}
			case "content":
				item["content"] = []any{map[string]any{"type": "reasoning_text", "text": syntheticKey}}
			case "encrypted_content":
				item["encrypted_content"] = syntheticKey
			}
			body := marshalReasoningResponse(t, responseWithReasoning(t, item))
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			result, err := (runner{transport: transport}).run(context.Background(), testOptions("openai"), "What changes?", testPack())
			if err == nil || !strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), syntheticKey) ||
				result.Proposal != nil || result.Preview != nil {
				t.Fatalf("ignored reasoning bypassed credential scanning or leaked output: %v", err)
			}
		})
	}
}
