// Package store is swissd's audit log, and the staging area plans pass through
// between compose, diff and apply.
//
// It is not a record of what is deployed and nothing reads it as one. That
// answer lives in the plan ConfigMap beside each release: etcd is replicated and
// backed up, a sqlite file on one PVC is neither, and two stores of the same
// fact disagree the moment an apply fails between them. So the cluster is the
// only source of truth, and losing this database costs the operation log --
// never the ability to see or upgrade what is running.
//
// One swissd serves one cluster and owns one database, so nothing here is keyed
// by cluster. Seeing several clusters at once is a link in the web nav, not a
// query.
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
  namespace  TEXT NOT NULL,
  release    TEXT NOT NULL,
  model      TEXT NOT NULL,
  variant    TEXT NOT NULL,
  document   TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
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
CREATE INDEX IF NOT EXISTS runs_release ON runs (namespace, release, id DESC);
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
func (s *Store) PutPlan(ctx context.Context, p *plan.Plan) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO plans (hash, namespace, release, model, variant, document, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Hash, p.Release.Namespace, p.Release.Name, p.Source.Model, p.Source.Variant,
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

// Run is one attempted operation. Append-only, and the only record that a diff
// changed nothing or that an apply failed -- none of which is cluster state, so
// nothing can rebuild it. It is also where a release's history lives now that
// no table claims to know what is deployed: the applies for one release, newest
// first, each naming the plan hash it ran.
type Run struct {
	ID        int64  `json:"id"`
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
		`INSERT INTO runs (namespace, release, action, plan_hash, actor, changed, error, output, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Namespace, r.Release, r.Action, r.PlanHash, r.Actor,
		boolInt(r.Changed), r.Error, r.Output, r.StartedAt, r.EndedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, namespace, release, action, plan_hash, actor, changed, error, started_at, ended_at
		 FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var changed int
		if err := rows.Scan(&r.ID, &r.Namespace, &r.Release, &r.Action, &r.PlanHash,
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
