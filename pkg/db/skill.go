package db

import (
	"context"
	"encoding/json"
	"time"
)

type SkillDAO interface {
	ListAddedSkills(ctx context.Context) ([]AddedSkill, error)
	AddSkill(ctx context.Context, skill AddedSkill) error
	RemoveSkill(ctx context.Context, catalogRef, uri string) error
}

// CatalogSkill is one SEP-2640 entry of a catalog, stored as JSON.
type CatalogSkill struct {
	ID         *int64          `db:"id" json:"id"`
	CatalogRef string          `db:"catalog_ref" json:"catalog_ref"`
	URI        string          `db:"uri" json:"uri"`
	Entry      json.RawMessage `db:"entry" json:"entry"`
}

// AddedSkill is a user approval bound to a manifest digest.
type AddedSkill struct {
	CatalogRef     string     `db:"catalog_ref"`
	URI            string     `db:"uri"`
	ManifestDigest string     `db:"manifest_digest"`
	AddedAt        *time.Time `db:"added_at"`
}

func (d *dao) ListAddedSkills(ctx context.Context) ([]AddedSkill, error) {
	const query = `SELECT catalog_ref, uri, manifest_digest, added_at FROM skill_added ORDER BY catalog_ref, uri`
	var skills []AddedSkill
	if err := d.db.SelectContext(ctx, &skills, query); err != nil {
		return nil, err
	}
	return skills, nil
}

func (d *dao) AddSkill(ctx context.Context, skill AddedSkill) error {
	const query = `INSERT INTO skill_added (catalog_ref, uri, manifest_digest, added_at) VALUES ($1, $2, $3, current_timestamp)
	ON CONFLICT (catalog_ref, uri) DO UPDATE SET manifest_digest = excluded.manifest_digest, added_at = current_timestamp`
	_, err := d.db.ExecContext(ctx, query, skill.CatalogRef, skill.URI, skill.ManifestDigest)
	return err
}

func (d *dao) RemoveSkill(ctx context.Context, catalogRef, uri string) error {
	const query = `DELETE FROM skill_added WHERE catalog_ref = $1 AND uri = $2`
	_, err := d.db.ExecContext(ctx, query, catalogRef, uri)
	return err
}

func (d *dao) listCatalogSkills(ctx context.Context, catalogRef string) ([]CatalogSkill, error) {
	const query = `SELECT id, catalog_ref, uri, entry FROM catalog_skill WHERE catalog_ref = $1 ORDER BY uri`
	var skills []CatalogSkill
	if err := d.db.SelectContext(ctx, &skills, query, catalogRef); err != nil {
		return nil, err
	}
	return skills, nil
}
