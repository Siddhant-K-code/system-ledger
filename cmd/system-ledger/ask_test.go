package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhant-K-code/system-ledger/internal/assist"
)

func askProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "examples", "go-http")
	if err := filepath.WalkDir(fixture, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"scan", "build"} {
		var out, errOut bytes.Buffer
		if err := run([]string{command, "--project", root}, &out, &errOut); err != nil {
			t.Fatalf("%s failed: %v %s", command, err, errOut.String())
		}
	}
	return root
}

func TestAskPreviewIsOfflineAndContainsActualRequest(t *testing.T) {
	const syntheticKey = "synthetic-secret-key-do-not-disclose"
	t.Setenv("OPENAI_API_KEY", syntheticKey)
	t.Setenv("ANTHROPIC_API_KEY", syntheticKey)
	root := askProject(t)
	for _, provider := range []string{"openai", "anthropic"} {
		for _, format := range []string{"text", "json"} {
			var out, errOut bytes.Buffer
			err := run([]string{"ask", "--project", root, "--asset", "GET /reports", "--provider", provider,
				"--model", "test-compatible-model", "--dry-run", "--format", format, "What changes for a report field?"}, &out, &errOut)
			if err != nil {
				t.Fatalf("preview %s/%s: %v %s", provider, format, err, errOut.String())
			}
			if strings.Contains(out.String()+errOut.String(), syntheticKey) {
				t.Fatal("credential leaked in preview")
			}
			if !strings.Contains(out.String(), "test-compatible-model") || !strings.Contains(out.String(), "reporting-platform") {
				t.Fatalf("request context missing: %s", out.String())
			}
			if format == "json" {
				var result assist.Result
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Preview == nil || !json.Valid(result.Preview.Body) {
					t.Fatalf("invalid JSON preview: %v %s", err, out.String())
				}
			}
		}
	}
	db, err := os.ReadFile(filepath.Join(root, ".system-ledger", "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(db, []byte(syntheticKey)) {
		t.Fatal("credential written to ledger")
	}
}

func TestAskRejectsNoConsentNoKeyAndStaleSources(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	root := askProject(t)
	base := []string{"ask", "--project", root, "--asset", "GET /reports", "--provider", "openai", "--model", "test-model"}
	for _, test := range []struct {
		option, want string
	}{
		{"", "--allow-remote"},
		{"--allow-remote", "OPENAI_API_KEY"},
	} {
		var out, errOut bytes.Buffer
		args := append([]string(nil), base...)
		if test.option != "" {
			args = append(args, test.option)
		}
		err := run(append(args, "question"), &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("unexpected preflight error: %v", err)
		}
	}
	path := filepath.Join(root, "services", "reports", "new.go")
	if err := os.WriteFile(path, []byte("package reports\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := append(append([]string(nil), base...), "--dry-run", "question")
	if err := run(args, &out, &errOut); err == nil || !strings.Contains(err.Error(), "rerun scan") {
		t.Fatalf("stale pack accepted: %v", err)
	}
}

func TestProposalTextAndJSONShowKnownAssetsAndOwners(t *testing.T) {
	owner := assist.Owner{Name: "reports", Team: "reporting-team"}
	pack := assist.Pack{Assets: []assist.Asset{{ID: "asset:one", Name: "reports.loadReports", Kind: "function", Owners: []assist.Owner{owner}}}}
	proposal := &assist.Proposal{
		Label: assist.UnverifiedLabel, Provider: "openai", Model: "test-model",
		Suggestions: []assist.Suggestion{{Text: "Review the projection.", AssetIDs: []string{"asset:one"}, EvidenceIDs: []string{"evidence:one"}}},
		Assumptions: []string{"A new field is required."}, UnansweredQuestions: []string{"What is the field type?"},
		AffectedOwners: []assist.Owner{owner},
	}
	for _, format := range []string{"text", "json"} {
		var out bytes.Buffer
		if err := renderProposal(&out, format, proposal, pack); err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"reports.loadReports", "reporting-team", "evidence:one"} {
			if !strings.Contains(out.String(), expected) {
				t.Fatalf("missing %s: %s", expected, out.String())
			}
		}
		if format == "json" && !json.Valid(out.Bytes()) {
			t.Fatal("invalid JSON proposal report")
		}
	}
}
