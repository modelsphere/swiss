// Package store is swissd's database: drafts, immutable plans, and the audit
// log. It is never the source of truth for what is deployed -- every applied
// plan is also written beside its release, so this can be rebuilt from the
// cluster.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aceforeverd/swiss/internal/plan"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS plans (
  hash       TEXT PRIMARY KEY,
  cluster    TEXT NOT NULL,
  namespace  TEXT NOT NULL,
  release    TEXT NOT NULL,
  model      TEXT NOT NULL,
  variant    TEXT NOT NULL,
  document   TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
  cluster    TEXT NOT NULL,
  namespace  TEXT NOT NULL,
  release    TEXT NOT NULL,
  plan_hash  TEXT NOT NULL,
  revision   INTEGER NOT NULL DEFAULT 0,
  version    INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (cluster, namespace, release)
);
CREATE TABLE IF NOT EXISTS runs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  cluster    TEXT NOT NULL,
  namespace  TEXT NOT NULL,
  release    TEXT NOT NULL,
  action     TEXT NOT NULL,
  plan_hash  TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  changed    INTEGER NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT '',
  output     TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  ended_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_release ON runs (cluster, namespace, release, id DESC);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// One writer; WAL plus a busy timeout is what keeps concurrent requests off
	// each other rather than a connection pool.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// PutPlan stores a plan. Plans are immutable: re-storing the same hash is a
// no-op, which is what makes "revert to the plan applied on the 3rd" work.
func (s *Store) PutPlan(ctx context.Context, cluster string, p *plan.Plan) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO plans (hash, cluster, namespace, release, model, variant, document, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Hash, cluster, p.Release.Namespace, p.Release.Name, p.Source.Model, p.Source.Variant,
		string(doc), now())
	return err
}

func (s *Store) Plan(ctx context.Context, hash string) (*plan.Plan, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM plans WHERE hash = ?`, hash).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no plan %s", hash)
	}
	if err != nil {
		return nil, err
	}
	var p plan.Plan
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

type Deployment struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Release   string `json:"release"`
	PlanHash  string `json:"planHash"`
	Revision  int    `json:"revision"`
	Version   int    `json:"version"`
	UpdatedAt string `json:"updatedAt"`
}

// RecordApply moves a deployment to a new plan. version is the row the caller
// read; a mismatch means someone else applied in between.
func (s *Store) RecordApply(ctx context.Context, d Deployment, expectVersion int) error {
	if expectVersion == 0 {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO deployments (cluster, namespace, release, plan_hash, revision, version, updated_at)
			 VALUES (?, ?, ?, ?, ?, 1, ?)
			 ON CONFLICT (cluster, namespace, release) DO UPDATE SET
			   plan_hash = excluded.plan_hash, revision = excluded.revision,
			   version = deployments.version + 1, updated_at = excluded.updated_at`,
			d.Cluster, d.Namespace, d.Release, d.PlanHash, d.Revision, now())
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET plan_hash = ?, revision = ?, version = version + 1, updated_at = ?
		 WHERE cluster = ? AND namespace = ? AND release = ? AND version = ?`,
		d.PlanHash, d.Revision, now(), d.Cluster, d.Namespace, d.Release, expectVersion)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("deployment changed since it was read; re-diff before applying")
	}
	return nil
}

func (s *Store) Deployments(ctx context.Context, cluster string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cluster, namespace, release, plan_hash, revision, version, updated_at
		 FROM deployments WHERE cluster = ? ORDER BY namespace, release`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.Cluster, &d.Namespace, &d.Release, &d.PlanHash, &d.Revision, &d.Version, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type Run struct {
	ID        int64  `json:"id"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Release   string `json:"release"`
	Action    string `json:"action"`
	PlanHash  string `json:"planHash"`
	Actor     string `json:"actor,omitempty"`
	Changed   bool   `json:"changed"`
	Error     string `json:"error,omitempty"`
	Output    string `json:"output,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
}

func (s *Store) RecordRun(ctx context.Context, r Run) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (cluster, namespace, release, action, plan_hash, actor, changed, error, output, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Cluster, r.Namespace, r.Release, r.Action, r.PlanHash, r.Actor,
		boolInt(r.Changed), r.Error, r.Output, r.StartedAt, r.EndedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Runs(ctx context.Context, cluster string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, cluster, namespace, release, action, plan_hash, actor, changed, error, started_at, ended_at
		 FROM runs WHERE cluster = ? ORDER BY id DESC LIMIT ?`, cluster, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var changed int
		if err := rows.Scan(&r.ID, &r.Cluster, &r.Namespace, &r.Release, &r.Action, &r.PlanHash,
			&r.Actor, &changed, &r.Error, &r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		r.Changed = changed == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
