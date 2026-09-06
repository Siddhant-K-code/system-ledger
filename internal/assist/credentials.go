package assist

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// These are obvious credential markers, not a general-purpose secret detector.
// This check is deliberately independent of the environment so previews stay offline.
var credentialMarkers = regexp.MustCompile(
	`(?i)(?:sk-(?:proj-|ant-)[a-z0-9_-]{8,}|sk-[a-z0-9_-]{20,}|` +
		`gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|(?-i:(?:AKIA|ASIA)[A-Z0-9]{16})|` +
		`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|` +
		`(?:OPENAI_API_KEY|ANTHROPIC_API_KEY)["']?[\t ]*[:=][\t ]*["']?[a-z0-9._~+/-]{8,}|` +
		`Bearer[\t ]+[a-z0-9._~+/-]{20,})`,
)

func invalidModel(model string) bool {
	return !validString(model, MaxModelBytes, true) || strings.TrimSpace(model) != model ||
		strings.IndexFunc(model, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
		}) >= 0
}

func scanJSONStrings(data []byte, matches func(string) bool) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, errOutput
		}
		if value, ok := token.(string); ok && matches(value) {
			return true, nil
		}
	}
}

func validateInputCredentials(data []byte, model string) error {
	found, err := scanJSONStrings(data, credentialMarkers.MatchString)
	if err != nil {
		return errors.New("could not validate typed assistance input; no request was sent")
	}
	if found || credentialMarkers.MatchString(model) {
		return errors.New("assistance input contains an apparent credential; remove it from the question, model, or evidence pack before continuing; no request was sent")
	}
	return nil
}

func validateOutputCredentials(body []byte, text, key string) error {
	matches := func(value string) bool { return strings.Contains(value, key) }
	for _, data := range [][]byte{body, []byte(text)} {
		found, err := scanJSONStrings(data, matches)
		if err != nil {
			return errOutput
		}
		if found {
			return errors.New("provider output contains the API key; no suggestion was accepted")
		}
	}
	return nil
}
