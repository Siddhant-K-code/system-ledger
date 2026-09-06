package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhant-K-code/system-ledger/internal/ledger"
)

func TestAskConfigPrecedence(t *testing.T) {
	root := t.TempDir()
	config := "version: 1\nservices: []\nassistance:\n  provider: openai\n  model: project-model\n"
	if err := os.WriteFile(filepath.Join(root, ledger.ConfigFilename), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, provider, model, wantProvider, wantModel string
	}{
		{"defaults", "", "", "openai", "project-model"},
		{"provider", "anthropic", "", "anthropic", "project-model"},
		{"model", "", "override-model", "openai", "override-model"},
		{"both", "anthropic", "override-model", "anthropic", "override-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := askOptions{Asset: "selected", Provider: test.provider, Model: test.model, DryRun: true}
			if err := options.resolve(root, []string{"question"}); err != nil {
				t.Fatal(err)
			}
			if options.Provider != test.wantProvider || options.Model != test.wantModel {
				t.Fatalf("unexpected settings: %+v", options)
			}
		})
	}
}

func TestAskRejectsMissingScopeConsentAndBadOrdering(t *testing.T) {
	for _, options := range []askOptions{
		{Asset: "selected"},
		{Asset: "selected", DryRun: true, AllowRemote: true},
		{DryRun: true},
	} {
		if err := options.resolve(t.TempDir(), []string{"question"}); err == nil {
			t.Fatalf("invalid options accepted: %+v", options)
		}
	}
	options := askOptions{Asset: "selected", DryRun: true}
	if err := options.resolve(t.TempDir(), []string{"question", "--model", "misplaced"}); err == nil {
		t.Fatal("misordered flags accepted")
	}
}
