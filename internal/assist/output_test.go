package assist

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRejectInvalidProposalJSON(t *testing.T) {
	valid := validProposalJSON()
	cases := map[string]string{
		"malformed":             `{"suggestions":[`,
		"trailing value":        valid + `{}`,
		"trailing prose":        valid + "\nThis is unverified.",
		"markdown":              "```json\n" + valid + "\n```",
		"null":                  `null`,
		"array":                 `[]`,
		"missing fields":        `{"suggestions":[]}`,
		"unknown top field":     strings.TrimSuffix(valid, "}") + `,"owners":[]}`,
		"unknown nested field":  strings.Replace(valid, `"text":`, `"owners":["fake"],"text":`, 1),
		"case folded field":     strings.Replace(valid, `"suggestions"`, `"Suggestions"`, 1),
		"case folded nested":    strings.Replace(valid, `"text"`, `"TEXT"`, 1),
		"missing text":          strings.Replace(valid, `"text":"Consider checking the route contract.",`, "", 1),
		"missing assets":        strings.Replace(valid, `"asset_ids":["asset:a"],`, "", 1),
		"missing evidence":      strings.Replace(valid, `,"evidence_ids":["evidence:a"]`, "", 1),
		"null evidence":         strings.Replace(valid, `"evidence_ids":["evidence:a"]`, `"evidence_ids":null`, 1),
		"null assumptions":      strings.Replace(valid, `["Static evidence may be incomplete."]`, `null`, 1),
		"null questions":        strings.Replace(valid, `["Does a runtime query use this table?"]`, `null`, 1),
		"null text":             strings.Replace(valid, `"Consider checking the route contract."`, `null`, 1),
		"null asset":            strings.Replace(valid, `["asset:a"]`, `[null]`, 1),
		"blank text":            strings.Replace(valid, `"Consider checking the route contract."`, `" "`, 1),
		"numeric text":          strings.Replace(valid, `"Consider checking the route contract."`, `5`, 1),
		"empty assets":          strings.Replace(valid, `["asset:a"]`, `[]`, 1),
		"unknown asset":         strings.Replace(valid, `["asset:a"]`, `["asset:outside"]`, 1),
		"unknown evidence":      strings.Replace(valid, `["evidence:a"]`, `["evidence:outside"]`, 1),
		"wrong reference type":  strings.Replace(valid, `["asset:a"]`, `["evidence:a"]`, 1),
		"duplicate assets":      strings.Replace(valid, `["asset:a"]`, `["asset:a","asset:a"]`, 1),
		"duplicate evidence":    strings.Replace(valid, `["evidence:a"]`, `["evidence:a","evidence:a"]`, 1),
		"duplicate top key":     strings.TrimSuffix(valid, "}") + `,"suggestions":[]}`,
		"duplicate nested key":  strings.Replace(valid, `"text":`, `"text":"earlier","text":`, 1),
		"escaped duplicate key": strings.Replace(valid, `"text":`, `"\u0074ext":"earlier","text":`, 1),
		"empty assumption":      strings.Replace(valid, `"Static evidence may be incomplete."`, `""`, 1),
		"null question":         strings.Replace(valid, `"Does a runtime query use this table?"`, `null`, 1),
		"object assumption":     strings.Replace(valid, `"Static evidence may be incomplete."`, `{}`, 1),
		"object assets":         strings.Replace(valid, `["asset:a"]`, `{}`, 1),
		"invalid utf8":          strings.Replace(valid, "Consider", string([]byte{0xff}), 1),
		"oversized text":        strings.Replace(valid, "Consider checking the route contract.", strings.Repeat("x", MaxStringBytes+1), 1),
		"oversized JSON":        strings.Repeat(" ", MaxOutputBytes) + valid,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProposal(text, testPack(), testOptions("openai"), nil); err == nil {
				t.Fatal("invalid proposal accepted")
			} else if strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), "earlier") {
				t.Fatal("error echoed model output")
			}
		})
	}
}

func TestProposalArrayLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		max  int
		item any
	}{
		{"suggestions", MaxSuggestions, map[string]any{"text": "Proposal", "asset_ids": []string{"asset:a"}, "evidence_ids": []string{}}},
		{"assumptions", MaxAssumptions, "Assumption"},
		{"unanswered_questions", MaxUnansweredQuestions, "Question"},
	} {
		for _, size := range []int{test.max, test.max + 1} {
			output := map[string]any{"suggestions": []any{}, "assumptions": []any{}, "unanswered_questions": []any{}}
			items := make([]any, size)
			for i := range items {
				items[i] = test.item
			}
			output[test.name] = items
			data, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseProposal(string(data), testPack(), testOptions("openai"), nil)
			if (err == nil) != (size == test.max) {
				t.Errorf("%s size %d: %v", test.name, size, err)
			}
		}
	}
}

func TestEmptyProposalAndDerivedOwners(t *testing.T) {
	proposal, err := parseProposal(`{"suggestions":[],"assumptions":[],"unanswered_questions":[]}`, testPack(), testOptions("openai"), nil)
	if err != nil || proposal.Usage != nil || len(proposal.Suggestions) != 0 || len(proposal.AffectedOwners) != 0 {
		t.Fatalf("empty proposal incorrectly handled: %v", err)
	}
	pack := testPack()
	pack.Assets[0].Owners = append(pack.Assets[0].Owners, Owner{Name: "alpha", Team: "z"}, Owner{Name: "alpha", Team: "a"})
	text := strings.Replace(validProposalJSON(), `["asset:a"]`, `["asset:b","asset:a"]`, 1)
	proposal, err = parseProposal(text, pack, testOptions("anthropic"), nil)
	want := []Owner{{Name: "alpha", Team: "a"}, {Name: "alpha", Team: "z"}, {Name: "catalog", Team: "commerce"}, {Name: "storage", Team: "data"}}
	if err != nil || !reflect.DeepEqual(proposal.AffectedOwners, want) {
		t.Fatalf("owners not derived and sorted: %v, got %#v", err, proposal.AffectedOwners)
	}
}

func TestRejectUnsupportedOpenAIResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing status", func(r map[string]any) { delete(r, "status") }},
		{"incomplete", func(r map[string]any) { r["status"] = "incomplete" }},
		{"failed", func(r map[string]any) { r["status"] = "failed" }},
		{"canceled", func(r map[string]any) { r["status"] = "cancelled" }},
		{"queued", func(r map[string]any) { r["status"] = "queued" }},
		{"error", func(r map[string]any) { r["error"] = map[string]any{"message": syntheticKey} }},
		{"incomplete details", func(r map[string]any) { r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"} }},
		{"missing output", func(r map[string]any) { delete(r, "output") }},
		{"empty output", func(r map[string]any) { r["output"] = []any{} }},
		{"flattened output", func(r map[string]any) { r["output"] = nil; r["output_text"] = validProposalJSON() }},
		{"tool", func(r map[string]any) {
			r["output"] = append(r["output"].([]any), map[string]any{"type": "function_call", "name": "shell"})
		}},
		{"malformed reasoning", func(r map[string]any) { r["output"] = append(r["output"].([]any), map[string]any{"type": "reasoning"}) }},
		{"nonassistant", func(r map[string]any) { openAIMessage(r)["role"] = "user" }},
		{"message incomplete", func(r map[string]any) { openAIMessage(r)["status"] = "incomplete" }},
		{"missing message status", func(r map[string]any) { delete(openAIMessage(r), "status") }},
		{"missing content", func(r map[string]any) { delete(openAIMessage(r), "content") }},
		{"null content", func(r map[string]any) { openAIMessage(r)["content"] = nil }},
		{"empty content", func(r map[string]any) { openAIMessage(r)["content"] = []any{} }},
		{"refusal", func(r map[string]any) {
			openAIMessage(r)["content"] = []any{map[string]any{"type": "refusal", "refusal": "No"}}
		}},
		{"text plus refusal", func(r map[string]any) {
			openAIMessage(r)["content"] = append(openAIMessage(r)["content"].([]any), map[string]any{"type": "refusal", "refusal": "No"})
		}},
		{"wrong text type", func(r map[string]any) { openAIContent(r)["type"] = "text" }},
		{"missing text", func(r map[string]any) { delete(openAIContent(r), "text") }},
		{"null text", func(r map[string]any) { openAIContent(r)["text"] = nil }},
		{"numeric text", func(r map[string]any) { openAIContent(r)["text"] = 3 }},
		{"oversized text", func(r map[string]any) { openAIContent(r)["text"] = strings.Repeat("x", MaxOutputBytes+1) }},
		{"tool calls", func(r map[string]any) { openAIMessage(r)["tool_calls"] = []any{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := decodeObject(t, responseJSON(t, "openai", validProposalJSON()))
			test.change(response)
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseResponse("openai", data); err == nil {
				t.Fatal("unsupported OpenAI response accepted")
			} else if strings.Contains(err.Error(), syntheticKey) {
				t.Fatal("error echoed response")
			}
		})
	}
}

func openAIMessage(response map[string]any) map[string]any {
	return response["output"].([]any)[0].(map[string]any)
}

func openAIContent(response map[string]any) map[string]any {
	return openAIMessage(response)["content"].([]any)[0].(map[string]any)
}

func TestRejectUnsupportedAnthropicResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing type", func(r map[string]any) { delete(r, "type") }},
		{"missing role", func(r map[string]any) { delete(r, "role") }},
		{"wrong role", func(r map[string]any) { r["role"] = "user" }},
		{"missing stop reason", func(r map[string]any) { delete(r, "stop_reason") }},
		{"max tokens", func(r map[string]any) { r["stop_reason"] = "max_tokens" }},
		{"stop sequence", func(r map[string]any) { r["stop_reason"] = "stop_sequence" }},
		{"nonempty stop sequence", func(r map[string]any) { r["stop_sequence"] = "end" }},
		{"tool stop", func(r map[string]any) { r["stop_reason"] = "tool_use" }},
		{"refusal", func(r map[string]any) { r["stop_reason"] = "refusal" }},
		{"pause turn", func(r map[string]any) { r["stop_reason"] = "pause_turn" }},
		{"null stop", func(r map[string]any) { r["stop_reason"] = nil }},
		{"error", func(r map[string]any) { r["error"] = map[string]any{"message": syntheticKey} }},
		{"missing content", func(r map[string]any) { delete(r, "content") }},
		{"null content", func(r map[string]any) { r["content"] = nil }},
		{"empty content", func(r map[string]any) { r["content"] = []any{} }},
		{"tool content", func(r map[string]any) {
			r["content"] = append(r["content"].([]any), map[string]any{"type": "tool_use", "name": "shell", "input": map[string]any{}})
		}},
		{"thinking content", func(r map[string]any) {
			r["content"] = append(r["content"].([]any), map[string]any{"type": "thinking", "thinking": "reasoning"})
		}},
		{"refusal content", func(r map[string]any) { r["content"] = []any{map[string]any{"type": "refusal", "text": "No"}} }},
		{"null text", func(r map[string]any) { r["content"].([]any)[0].(map[string]any)["text"] = nil }},
		{"missing text", func(r map[string]any) { delete(r["content"].([]any)[0].(map[string]any), "text") }},
		{"object text", func(r map[string]any) { r["content"].([]any)[0].(map[string]any)["text"] = map[string]any{} }},
		{"oversized text", func(r map[string]any) {
			r["content"].([]any)[0].(map[string]any)["text"] = strings.Repeat("x", MaxOutputBytes+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := decodeObject(t, responseJSON(t, "anthropic", validProposalJSON()))
			test.change(response)
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseResponse("anthropic", data); err == nil {
				t.Fatal("unsupported Anthropic response accepted")
			}
		})
	}
}

func TestMalformedResponseEnvelopes(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		valid := string(responseJSON(t, provider, validProposalJSON()))
		for _, invalid := range []string{
			`{"`, `null`, `[]`, `{"status":"completed"}`, valid + `{}`,
			strings.TrimSuffix(valid, "}") + `,"usage":{}}`,
			strings.Repeat("[", 65) + strings.Repeat("]", 65),
			strings.Repeat(" ", MaxResponseBytes+1),
		} {
			if _, _, err := parseResponse(provider, []byte(invalid)); err == nil {
				t.Errorf("%s accepted malformed envelope", provider)
			}
		}
	}
}

func TestNestedTextFragmentsAndOptionalUsage(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		response := decodeObject(t, responseJSON(t, provider, validProposalJSON()))
		text := validProposalJSON()
		parts := []any{
			map[string]any{"type": "text", "text": text[:len(text)/2]},
			map[string]any{"type": "text", "text": text[len(text)/2:]},
		}
		if provider == "openai" {
			for _, part := range parts {
				part.(map[string]any)["type"] = "output_text"
			}
			openAIMessage(response)["content"] = parts
		} else {
			response["content"] = parts
		}
		delete(response, "usage")
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		got, usage, err := parseResponse(provider, data)
		if err != nil || got != text || usage != nil {
			t.Errorf("%s text fragments or missing usage mishandled: %v", provider, err)
		}
	}
}

func TestUsageIsActualAndValidated(t *testing.T) {
	for _, test := range []struct {
		text string
		ok   bool
	}{
		{`null`, true}, {`{}`, true}, {`{"input_tokens":0}`, true},
		{`{"output_tokens":2}`, true}, {`{"input_tokens":10,"output_tokens":20}`, true},
		{`{"input_tokens":-1}`, false}, {`{"output_tokens":1.2}`, false},
		{`{"input_tokens":"12"}`, false}, {`{"output_tokens":9223372036854775808}`, false},
		{`[]`, false}, {`true`, false},
	} {
		usage, err := parseUsage([]byte(test.text))
		if (err == nil) != test.ok {
			t.Errorf("%s: %v", test.text, err)
		}
		if test.text == `{"input_tokens":0}` && (usage == nil || usage.InputTokens == nil || *usage.InputTokens != 0 || usage.OutputTokens != nil) {
			t.Fatal("missing usage was invented or actual zero was discarded")
		}
	}
}
