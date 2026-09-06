package assist

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"
)

var attributeNames = map[string]bool{
	"package": true, "receiver": true, "method": true, "path": true,
	"pattern": true, "operation": true, "table": true, "openapi": true,
	"asyncapi": true, "domain": true,
}

func validString(value string, limit int, required bool) bool {
	return len(value) <= limit && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func validatePack(pack Pack) error {
	if len(pack.Assets) == 0 || len(pack.Assets) > MaxAssets ||
		len(pack.Evidence) > MaxEvidence || len(pack.Relationships) > MaxRelationships ||
		len(pack.Gaps) > MaxGaps {
		return errors.New("evidence pack exceeds item limits or has no assets; select a narrower asset")
	}
	assets := make(map[string]bool)
	evidence := make(map[string]bool)
	ids := make(map[string]bool)
	addID := func(id string) bool {
		if !validString(id, MaxIDBytes, true) || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for _, asset := range pack.Assets {
		if !addID(asset.ID) || !validString(asset.Kind, MaxStringBytes, true) ||
			!validString(asset.Name, MaxStringBytes, true) ||
			len(asset.Attributes) > MaxAttributes || len(asset.Owners) > MaxOwners {
			return errors.New("evidence pack contains an invalid or oversized asset")
		}
		assets[asset.ID] = true
		for name, value := range asset.Attributes {
			if !attributeNames[name] || !validString(value, MaxStringBytes, false) {
				return errors.New("evidence pack contains an unsupported attribute; only extracted fact attributes are allowed")
			}
		}
		for _, owner := range asset.Owners {
			if !validString(owner.Name, MaxStringBytes, true) || !validString(owner.Team, MaxStringBytes, false) {
				return errors.New("evidence pack contains an invalid owner")
			}
		}
	}
	if !assets[pack.SelectedAssetID] {
		return errors.New("evidence pack selected asset is not in the transmitted assets")
	}
	for _, item := range pack.Evidence {
		if !addID(item.ID) || !validString(item.Source, MaxStringBytes, true) ||
			!validString(item.Locator, MaxStringBytes, true) || len(item.SHA256) != 64 {
			return errors.New("evidence pack contains invalid source evidence")
		}
		if _, err := hex.DecodeString(item.SHA256); err != nil {
			return errors.New("evidence pack contains an invalid SHA-256 digest")
		}
		evidence[item.ID] = true
	}
	for _, asset := range pack.Assets {
		if !validReferences(asset.EvidenceIDs, evidence, MaxEvidence, false) {
			return errors.New("evidence pack asset references unknown or duplicate evidence")
		}
	}
	for _, relationship := range pack.Relationships {
		if !addID(relationship.ID) || !assets[relationship.FromID] || !assets[relationship.ToID] ||
			!validString(relationship.Type, MaxStringBytes, true) ||
			!validString(relationship.Origin, MaxStringBytes, true) ||
			!validReferences(relationship.EvidenceIDs, evidence, MaxEvidence, false) {
			return errors.New("evidence pack contains an invalid relationship or reference")
		}
	}
	for _, gap := range pack.Gaps {
		if !validString(gap.Code, MaxStringBytes, true) || !validString(gap.Message, MaxStringBytes, true) ||
			!validString(gap.Source, MaxStringBytes, false) || !validString(gap.Locator, MaxStringBytes, false) {
			return errors.New("evidence pack contains an invalid gap")
		}
	}
	return nil
}

func validReferences(refs []string, known map[string]bool, maximum int, required bool) bool {
	if len(refs) > maximum || (required && len(refs) == 0) {
		return false
	}
	seen := make(map[string]bool)
	for _, id := range refs {
		if !known[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func normalizedPack(pack Pack) Pack {
	pack.Assets = append([]Asset{}, pack.Assets...)
	pack.Evidence = append([]Evidence{}, pack.Evidence...)
	pack.Relationships = append([]Relationship{}, pack.Relationships...)
	pack.Gaps = append([]Gap{}, pack.Gaps...)
	for i := range pack.Assets {
		asset := &pack.Assets[i]
		if asset.Attributes == nil {
			asset.Attributes = map[string]string{}
		}
		asset.Owners = append([]Owner{}, asset.Owners...)
		asset.EvidenceIDs = append([]string{}, asset.EvidenceIDs...)
	}
	for i := range pack.Relationships {
		pack.Relationships[i].EvidenceIDs = append([]string{}, pack.Relationships[i].EvidenceIDs...)
	}
	return pack
}
