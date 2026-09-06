package assist

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

var errOutput = errors.New("provider returned an invalid, unsupported, or incomplete proposal; no suggestion was accepted")

// Check duplicate keys before decoding: encoding/json otherwise silently keeps
// the last value. Provider metadata may evolve, but proposal fields are exact.
func checkJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errOutput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkJSONValue(decoder, 0); err != nil {
		return errOutput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errOutput
	}
	return nil
}

func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errOutput
	}
	token, err := decoder.Token()
	if err != nil {
		return errOutput
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] {
				return errOutput
			}
			keys[key] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errOutput
	}
	end, err := decoder.Token()
	if err != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return errOutput
	}
	return nil
}

func object(data json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, errOutput
	}
	return fields, nil
}

func present(data json.RawMessage) bool {
	return len(data) != 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func array(data json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if !present(data) || json.Unmarshal(data, &items) != nil || items == nil {
		return nil, errOutput
	}
	return items, nil
}

func stringValue(data json.RawMessage) (string, error) {
	var text string
	if !present(data) || json.Unmarshal(data, &text) != nil {
		return "", errOutput
	}
	return text, nil
}

func equals(data json.RawMessage, want string) bool {
	got, err := stringValue(data)
	return err == nil && got == want
}

func exactFields(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if !present(fields[name]) {
			return false
		}
	}
	return true
}

func parseResponse(provider string, data []byte) (string, *Usage, error) {
	if len(data) > MaxResponseBytes || checkJSON(data) != nil {
		return "", nil, errOutput
	}
	fields, err := object(data)
	if err != nil {
		return "", nil, errOutput
	}
	var parts []json.RawMessage
	if provider == "openai" {
		if !equals(fields["status"], "completed") || present(fields["error"]) || present(fields["incomplete_details"]) {
			return "", nil, errOutput
		}
		items, err := array(fields["output"])
		if err != nil || len(items) == 0 || len(items) > MaxSuggestions {
			return "", nil, errOutput
		}
		for _, raw := range items {
			message, err := object(raw)
			if err != nil {
				return "", nil, errOutput
			}
			if equals(message["type"], "reasoning") {
				if !validReasoningItem(message) {
					return "", nil, errOutput
				}
				continue
			}
			if !equals(message["type"], "message") || !equals(message["role"], "assistant") ||
				!equals(message["status"], "completed") || present(message["tool_calls"]) || present(message["refusal"]) {
				return "", nil, errOutput
			}
			content, err := array(message["content"])
			if err != nil || len(content) == 0 || len(content) > MaxSuggestions {
				return "", nil, errOutput
			}
			parts = append(parts, content...)
		}
	} else {
		if !equals(fields["type"], "message") || !equals(fields["role"], "assistant") ||
			!equals(fields["stop_reason"], "end_turn") || present(fields["stop_sequence"]) || present(fields["error"]) {
			return "", nil, errOutput
		}
		parts, err = array(fields["content"])
		if err != nil || len(parts) == 0 || len(parts) > MaxSuggestions {
			return "", nil, errOutput
		}
	}
	if len(parts) == 0 {
		return "", nil, errOutput
	}
	var output strings.Builder
	for _, raw := range parts {
		part, err := object(raw)
		wantType := "text"
		if provider == "openai" {
			wantType = "output_text"
		}
		if err != nil || !equals(part["type"], wantType) || present(part["refusal"]) {
			return "", nil, errOutput
		}
		text, err := stringValue(part["text"])
		if err != nil || text == "" || output.Len()+len(text) > MaxOutputBytes {
			return "", nil, errOutput
		}
		output.WriteString(text)
	}
	usage, err := parseUsage(fields["usage"])
	if err != nil {
		return "", nil, err
	}
	return output.String(), usage, nil
}

// Reasoning is provider metadata, never proposal text. A missing item status is
// valid in completed Responses; an explicitly unfinished status is not.
func validReasoningItem(item map[string]json.RawMessage) bool {
	for name := range item {
		switch name {
		case "id", "type", "summary", "status", "content", "encrypted_content":
		default:
			return false
		}
	}
	id, err := stringValue(item["id"])
	if err != nil || !validString(id, MaxIDBytes, true) || !validReasoningText(item["summary"], "summary_text") {
		return false
	}
	if status, exists := item["status"]; exists && !equals(status, "completed") {
		return false
	}
	if content, exists := item["content"]; exists && !validReasoningText(content, "reasoning_text") {
		return false
	}
	if present(item["encrypted_content"]) {
		if _, err := stringValue(item["encrypted_content"]); err != nil {
			return false
		}
	}
	return true
}

func validReasoningText(data json.RawMessage, kind string) bool {
	parts, err := array(data)
	if err != nil || len(parts) > MaxSuggestions {
		return false
	}
	for _, raw := range parts {
		part, err := object(raw)
		if err != nil || !exactFields(part, "type", "text") || !equals(part["type"], kind) {
			return false
		}
		if _, err := stringValue(part["text"]); err != nil {
			return false
		}
	}
	return true
}

func parseUsage(data json.RawMessage) (*Usage, error) {
	if !present(data) {
		return nil, nil
	}
	fields, err := object(data)
	if err != nil {
		return nil, errOutput
	}
	var usage Usage
	for name, target := range map[string]**int64{
		"input_tokens": &usage.InputTokens, "output_tokens": &usage.OutputTokens,
	} {
		if !present(fields[name]) {
			continue
		}
		var value int64
		if json.Unmarshal(fields[name], &value) != nil || value < 0 {
			return nil, errOutput
		}
		*target = &value
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil {
		return nil, nil
	}
	return &usage, nil
}

func parseProposal(text string, pack Pack, options Options, usage *Usage) (Proposal, error) {
	if len(text) > MaxOutputBytes || checkJSON([]byte(text)) != nil {
		return Proposal{}, errOutput
	}
	fields, err := object([]byte(text))
	if err != nil || !exactFields(fields, "suggestions", "assumptions", "unanswered_questions") {
		return Proposal{}, errOutput
	}
	items, err := array(fields["suggestions"])
	if err != nil || len(items) > MaxSuggestions {
		return Proposal{}, errOutput
	}
	proposal := Proposal{
		Label: UnverifiedLabel, Provider: options.Provider, Model: options.Model,
		Suggestions: []Suggestion{}, AffectedOwners: []Owner{}, Usage: usage,
	}
	assets := make(map[string]bool)
	evidence := make(map[string]bool)
	affected := make(map[string]bool)
	for _, asset := range pack.Assets {
		assets[asset.ID] = true
	}
	for _, item := range pack.Evidence {
		evidence[item.ID] = true
	}
	for _, raw := range items {
		item, err := object(raw)
		if err != nil || !exactFields(item, "text", "asset_ids", "evidence_ids") {
			return Proposal{}, errOutput
		}
		var suggestion Suggestion
		if json.Unmarshal(raw, &suggestion) != nil ||
			!validString(suggestion.Text, MaxStringBytes, true) ||
			!validReferences(suggestion.AssetIDs, assets, MaxAssets, true) ||
			!validReferences(suggestion.EvidenceIDs, evidence, MaxEvidence, false) {
			return Proposal{}, errOutput
		}
		for _, id := range suggestion.AssetIDs {
			affected[id] = true
		}
		proposal.Suggestions = append(proposal.Suggestions, suggestion)
	}
	proposal.Assumptions, err = parseStrings(fields["assumptions"], MaxAssumptions)
	if err != nil {
		return Proposal{}, err
	}
	proposal.UnansweredQuestions, err = parseStrings(fields["unanswered_questions"], MaxUnansweredQuestions)
	if err != nil {
		return Proposal{}, err
	}
	seenOwners := make(map[Owner]bool)
	for _, asset := range pack.Assets {
		if !affected[asset.ID] {
			continue
		}
		for _, owner := range asset.Owners {
			if !seenOwners[owner] {
				proposal.AffectedOwners = append(proposal.AffectedOwners, owner)
				seenOwners[owner] = true
			}
		}
	}
	sort.Slice(proposal.AffectedOwners, func(i, j int) bool {
		a, b := proposal.AffectedOwners[i], proposal.AffectedOwners[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Team < b.Team
	})
	return proposal, nil
}

func parseStrings(data json.RawMessage, maximum int) ([]string, error) {
	items, err := array(data)
	if err != nil || len(items) > maximum {
		return nil, errOutput
	}
	result := make([]string, 0, len(items))
	for _, raw := range items {
		value, err := stringValue(raw)
		if err != nil || !validString(value, MaxStringBytes, true) {
			return nil, errOutput
		}
		result = append(result, value)
	}
	return result, nil
}
