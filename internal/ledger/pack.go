package ledger

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/Siddhant-K-code/system-ledger/internal/assist"
)

const (
	packDepth         = 4
	packAssets        = assist.MaxAssets
	packRelationships = assist.MaxRelationships
	packEvidence      = assist.MaxEvidence
	packGaps          = assist.MaxGaps
)

type EvidencePack = assist.Pack
type EvidenceOwner = assist.Owner
type EvidenceAsset = assist.Asset
type EvidenceRelationship = assist.Relationship
type SourceEvidence = assist.Evidence
type EvidenceGap = assist.Gap

// BuildEvidencePack exposes typed facts only. It never reads source excerpts,
// repository prose, environment variables, or provider settings.
func BuildEvidencePack(db *sql.DB, root, query string) (EvidencePack, error) {
	tx, err := db.Begin()
	if err != nil {
		return EvidencePack{}, err
	}
	defer tx.Rollback()
	return buildEvidencePack(tx, root, query)
}

func buildEvidencePack(db queryer, root, query string) (EvidencePack, error) {
	if filepath.Clean(root) != filepath.Clean(projectRoot(db)) {
		return EvidencePack{}, fmt.Errorf("ledger belongs to a different project root; scan this project first")
	}
	if err := Verify(db); err != nil {
		return EvidencePack{}, err
	}
	selected, err := findAsset(db, query)
	if err != nil {
		return EvidencePack{}, err
	}
	pack := EvidencePack{
		Assets: []EvidenceAsset{}, Relationships: []EvidenceRelationship{},
		Evidence: []SourceEvidence{}, Gaps: []EvidenceGap{},
	}
	depths := map[int64]int{selected.ID: 0}
	queue := []int64{selected.ID}
	boundary := false
	for index := 0; index < len(queue); index++ {
		current := queue[index]
		rows, err := db.Query(`SELECT DISTINCT a.id FROM relationships r
			JOIN assets a ON a.id = CASE WHEN r.from_asset_id = ? THEN r.to_asset_id ELSE r.from_asset_id END
			JOIN sources s ON s.id = a.source_id
			WHERE (r.from_asset_id = ? OR r.to_asset_id = ?) AND r.relationship_type NOT IN ('owns_asset', 'contains')
			ORDER BY a.kind, a.canonical_name, s.path LIMIT ?`, current, current, current, packAssets+1)
		if err != nil {
			return EvidencePack{}, err
		}
		for rows.Next() {
			var next int64
			if err := rows.Scan(&next); err != nil {
				rows.Close()
				return EvidencePack{}, err
			}
			if _, seen := depths[next]; seen {
				continue
			}
			if depths[current] == packDepth || len(queue) == packAssets {
				boundary = true
				continue
			}
			depths[next] = depths[current] + 1
			queue = append(queue, next)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return EvidencePack{}, err
		}
	}
	stableIDs := make(map[int64]string)
	references := make(map[string]SourceEvidence)
	sourcePaths := make(map[string]bool)
	addEvidence := func(column string, id int64) ([]string, error) {
		if column != "asset_id" && column != "relationship_id" {
			return nil, fmt.Errorf("invalid evidence target")
		}
		rows, err := db.Query(`SELECT s.path, s.sha256, e.locator FROM evidence e JOIN sources s ON s.id = e.source_id
			WHERE e.`+column+` = ? ORDER BY s.path, e.locator`, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var ref SourceEvidence
			if err := rows.Scan(&ref.Source, &ref.SHA256, &ref.Locator); err != nil {
				return nil, err
			}
			ref.ID = stableID("evidence", ref.Source, ref.SHA256, ref.Locator)
			references[ref.ID] = ref
			sourcePaths[ref.Source] = true
			ids = append(ids, ref.ID)
			if len(references) > packEvidence {
				return nil, fmt.Errorf("evidence exceeds %d locations; select a narrower asset", packEvidence)
			}
		}
		sort.Strings(ids)
		return ids, rows.Err()
	}
	for _, id := range queue {
		var asset EvidenceAsset
		var source, canonical, attributes string
		err := db.QueryRow(`SELECT a.kind, a.name, a.canonical_name, a.attributes, s.path
			FROM assets a JOIN sources s ON s.id = a.source_id WHERE a.id = ?`, id).
			Scan(&asset.Kind, &asset.Name, &canonical, &attributes, &source)
		if err != nil {
			return EvidencePack{}, err
		}
		asset.ID = stableID("asset", asset.Kind, source, canonical)
		stableIDs[id] = asset.ID
		if id == selected.ID {
			pack.SelectedAssetID = asset.ID
		}
		var attrs map[string]string
		if err := json.Unmarshal([]byte(attributes), &attrs); err != nil {
			return EvidencePack{}, fmt.Errorf("invalid extracted attributes")
		}
		asset.Attributes = make(map[string]string)
		for _, key := range []string{"package", "receiver", "method", "path", "pattern", "operation", "table", "openapi", "asyncapi", "domain"} {
			if value, ok := attrs[key]; ok {
				asset.Attributes[key] = value
			}
		}
		asset.EvidenceIDs, err = addEvidence("asset_id", id)
		if err != nil {
			return EvidencePack{}, err
		}
		asset.Owners, err = packOwners(db, id)
		if err != nil {
			return EvidencePack{}, err
		}
		ownerRows, err := db.Query(`SELECT id, from_asset_id FROM relationships WHERE to_asset_id = ? AND relationship_type = 'owns_asset' ORDER BY id`, id)
		if err != nil {
			return EvidencePack{}, err
		}
		var ownershipIDs [][2]int64
		for ownerRows.Next() {
			var owner [2]int64
			if err := ownerRows.Scan(&owner[0], &owner[1]); err != nil {
				ownerRows.Close()
				return EvidencePack{}, err
			}
			ownershipIDs = append(ownershipIDs, owner)
		}
		err = ownerRows.Err()
		ownerRows.Close()
		if err != nil {
			return EvidencePack{}, err
		}
		for _, owner := range ownershipIDs {
			ids, err := addEvidence("relationship_id", owner[0])
			if err != nil {
				return EvidencePack{}, err
			}
			asset.EvidenceIDs = append(asset.EvidenceIDs, ids...)
			ids, err = addEvidence("asset_id", owner[1])
			if err != nil {
				return EvidencePack{}, err
			}
			asset.EvidenceIDs = append(asset.EvidenceIDs, ids...)
		}
		asset.EvidenceIDs = sortedUnique(asset.EvidenceIDs)
		pack.Assets = append(pack.Assets, asset)
	}
	type relation struct {
		id, from, to int64
		kind, origin string
	}
	rows, err := db.Query(`SELECT id, from_asset_id, to_asset_id, relationship_type, origin
		FROM relationships WHERE relationship_type NOT IN ('owns_asset', 'contains') ORDER BY relationship_type, origin, id`)
	if err != nil {
		return EvidencePack{}, err
	}
	var relations []relation
	for rows.Next() {
		var rel relation
		if err := rows.Scan(&rel.id, &rel.from, &rel.to, &rel.kind, &rel.origin); err != nil {
			rows.Close()
			return EvidencePack{}, err
		}
		if stableIDs[rel.from] != "" && stableIDs[rel.to] != "" {
			relations = append(relations, rel)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return EvidencePack{}, err
	}
	sort.Slice(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return stableID("relation", stableIDs[a.from], stableIDs[a.to], a.kind, a.origin) < stableID("relation", stableIDs[b.from], stableIDs[b.to], b.kind, b.origin)
	})
	for i, rel := range relations {
		if i == packRelationships {
			boundary = true
			break
		}
		edge := EvidenceRelationship{
			ID:     stableID("relation", stableIDs[rel.from], stableIDs[rel.to], rel.kind, rel.origin),
			FromID: stableIDs[rel.from], ToID: stableIDs[rel.to], Type: rel.kind, Origin: rel.origin,
		}
		edge.EvidenceIDs, err = addEvidence("relationship_id", rel.id)
		if err != nil {
			return EvidencePack{}, err
		}
		pack.Relationships = append(pack.Relationships, edge)
	}
	gaps, err := Diagnostics(db)
	if err != nil {
		return EvidencePack{}, err
	}
	for _, gap := range gaps {
		if sourcePaths[gap.Path] {
			if len(pack.Gaps) == packGaps-2 {
				boundary = true
				break
			}
			pack.Gaps = append(pack.Gaps, EvidenceGap{Code: gap.Code, Message: gap.Message, Source: gap.Path, Locator: gap.Locator})
		}
	}
	if boundary {
		pack.Gaps = append(pack.Gaps, EvidenceGap{Code: "context_boundary", Message: "Context is limited to four hops, 32 assets, 64 relationships and 64 gaps; omitted neighbors are not evidence of absence."})
	}
	if metadata(db, "last_build_at") == "" {
		pack.Gaps = append(pack.Gaps, EvidenceGap{Code: "not_built", Message: "No build has run since this scan; inferred associations are absent."})
	}
	for _, ref := range references {
		pack.Evidence = append(pack.Evidence, ref)
	}
	sort.Slice(pack.Assets, func(i, j int) bool { return pack.Assets[i].ID < pack.Assets[j].ID })
	sort.Slice(pack.Evidence, func(i, j int) bool { return pack.Evidence[i].ID < pack.Evidence[j].ID })
	return pack, nil
}

func stableID(kind string, parts ...string) string {
	encoded, _ := json.Marshal(parts)
	return kind + ":" + sourceHash(encoded)
}

func packOwners(db queryer, id int64) ([]EvidenceOwner, error) {
	rows, err := db.Query(`SELECT DISTINCT c.name, c.owner FROM service_configs c JOIN assets s ON s.name = c.name AND s.kind = 'service'
		WHERE s.id = ? OR s.id IN (SELECT from_asset_id FROM relationships WHERE to_asset_id = ? AND relationship_type = 'owns_asset')
		ORDER BY c.name, c.owner`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := []EvidenceOwner{}
	for rows.Next() {
		var owner EvidenceOwner
		if err := rows.Scan(&owner.Name, &owner.Team); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}
