package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/Siddhant-K-code/system-ledger/internal/assist"
	"github.com/Siddhant-K-code/system-ledger/internal/ledger"
)

func runAsk(db *sql.DB, root string, options askOptions, question string, out io.Writer, format string) error {
	pack, err := ledger.BuildEvidencePack(db, root, options.Asset)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := assist.Run(ctx, assist.Options{
		Provider: options.Provider, Model: options.Model,
		DryRun: options.DryRun, AllowRemote: options.AllowRemote,
	}, question, pack)
	if err != nil {
		return err
	}
	if result.Preview != nil {
		if format == "json" {
			return jsonOutput(out, result)
		}
		fmt.Fprintf(out, "Dry run: no request sent. Review before using --allow-remote.\nProvider: %s | Model: %s\nPOST %s\n%s\n",
			result.Preview.Provider, result.Preview.Model, result.Preview.Endpoint, result.Preview.Body)
		return nil
	}
	if result.Proposal == nil {
		return fmt.Errorf("assistance returned neither a preview nor a proposal")
	}
	return renderProposal(out, format, result.Proposal, pack)
}

func renderProposal(out io.Writer, format string, proposal *assist.Proposal, pack assist.Pack) error {
	affected := make(map[string]bool)
	for _, suggestion := range proposal.Suggestions {
		for _, id := range suggestion.AssetIDs {
			affected[id] = true
		}
	}
	assets := []assist.Asset{}
	names := make(map[string]string)
	for _, asset := range pack.Assets {
		names[asset.ID] = asset.Name
		if affected[asset.ID] {
			assets = append(assets, asset)
		}
	}
	if format == "json" {
		return jsonOutput(out, struct {
			Proposal *assist.Proposal `json:"proposal"`
			Assets   []assist.Asset   `json:"known_affected_assets"`
		}{proposal, assets})
	}
	fmt.Fprintf(out, "Unverified proposed explanation - review required.\nProvider: %s | Model: %s\nCitation existence does not prove correctness. The ledger was not changed.\n",
		proposal.Provider, proposal.Model)
	for i, suggestion := range proposal.Suggestions {
		fmt.Fprintf(out, "%d. %s\n", i+1, suggestion.Text)
		for _, id := range suggestion.AssetIDs {
			fmt.Fprintf(out, "   Asset: %s (%s)\n", names[id], id)
		}
		fmt.Fprintf(out, "   Evidence: %s\n", strings.Join(suggestion.EvidenceIDs, ", "))
	}
	for _, owner := range proposal.AffectedOwners {
		fmt.Fprintf(out, "Owner: %s (team: %s)\n", owner.Name, owner.Team)
	}
	for _, assumption := range proposal.Assumptions {
		fmt.Fprintf(out, "Assumption: %s\n", assumption)
	}
	for _, question := range proposal.UnansweredQuestions {
		fmt.Fprintf(out, "Unanswered: %s\n", question)
	}
	if proposal.Usage != nil {
		if proposal.Usage.InputTokens != nil {
			fmt.Fprintf(out, "Input tokens: %d\n", *proposal.Usage.InputTokens)
		}
		if proposal.Usage.OutputTokens != nil {
			fmt.Fprintf(out, "Output tokens: %d\n", *proposal.Usage.OutputTokens)
		}
	}
	return nil
}
