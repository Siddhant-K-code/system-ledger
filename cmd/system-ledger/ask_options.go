package main

import (
	"fmt"
	"strings"

	"github.com/Siddhant-K-code/system-ledger/internal/ledger"
)

type askOptions struct {
	Asset       string
	Provider    string
	Model       string
	DryRun      bool
	AllowRemote bool
}

func (options *askOptions) resolve(root string, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("ask requires one non-empty question; place all flags before the question")
	}
	if options.Asset == "" {
		return fmt.Errorf("ask requires --asset to select the evidence scope")
	}
	if options.DryRun == options.AllowRemote {
		return fmt.Errorf("ask requires exactly one of --dry-run or --allow-remote; no request was sent")
	}
	config, err := ledger.LoadConfig(root)
	if err != nil {
		return err
	}
	if options.Provider == "" {
		options.Provider = config.Assistance.Provider
	}
	if options.Model == "" {
		options.Model = config.Assistance.Model
	}
	if options.Provider != "openai" && options.Provider != "anthropic" {
		return fmt.Errorf("assistance provider must be openai or anthropic")
	}
	if strings.TrimSpace(options.Model) == "" {
		return fmt.Errorf("ask requires an explicit model via --model or assistance.model")
	}
	return nil
}
