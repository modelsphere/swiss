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
  note       TEXT NOT NULL DEFAULT '',
  revision   INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  ended_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_release ON runs (namespace, release, id DESC);
`

// added are columns that arrived after the first release. The CREATE above
// covers a new database; an existing one is altered here, because sqlite has no
// ADD COLUMN IF NOT EXISTS and a swissd upgrade must not need a fresh volume.
var added = []struct{ column, ddl string }{
	{"note", `ALTER TABLE runs ADD COLUMN note TEXT NOT NULL DEFAULT ''`},
	{"revision", `ALTER TABLE runs ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`},
}

// migrate adds those columns, reading what is there first rather than running
// each ALTER and ignoring the error it returns: "duplicate column" and a real
// failure are both errors, and telling them apart by message is how a broken
// database gets opened as a working one.
func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range added {
		if have[c.column] {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return fmt.Errorf("add runs.%s: %w", c.column, err)
		}
	}
	return nil
}

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
	if err := migrate(db); err != nil {
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
	// Note is why this was done, typed by whoever did it. The same text is
	// written beside the release in the cluster, so it survives losing this
	// database -- which is the one thing the log cannot do for itself.
	Note string `json:"note,omitempty"`
	// Revision the operation left the release at, 0 when it produced none: a
	// failed apply, an uninstall, or a row written before this was recorded.
	Revision  int    `json:"revision,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
}

func (s *Store) RecordRun(ctx context.Context, r Run) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (namespace, release, action, plan_hash, actor, changed, error, output, note, revision, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Namespace, r.Release, r.Action, r.PlanHash, r.Actor,
		boolInt(r.Changed), r.Error, r.Output, r.Note, r.Revision, r.StartedAt, r.EndedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RunFilter narrows the log. Zero values mean "everything", so the unfiltered
// call stays the simple one.
type RunFilter struct {
	Namespace string
	Release   string
	Action    string
	Limit     int
}

func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	return s.RunsFiltered(ctx, RunFilter{Limit: limit})
}

// RunsFiltered lists the log newest first. Output is deliberately not selected:
// helmfile output runs to tens of kilobytes per row, and a log view that drags
// every diff it ever rendered into one response is a log view nobody opens.
// One run's output comes from Run.
func (s *Store) RunsFiltered(ctx context.Context, f RunFilter) ([]Run, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT id, namespace, release, action, plan_hash, actor, changed, error, note, revision, started_at, ended_at
	      FROM runs WHERE 1 = 1`
	var args []any
	for _, c := range []struct {
		col string
		val string
	}{
		{"namespace", f.Namespace}, {"release", f.Release}, {"action", f.Action},
	} {
		if c.val != "" {
			q += " AND " + c.col + " = ?"
			args = append(args, c.val)
		}
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var changed int
		if err := rows.Scan(&r.ID, &r.Namespace, &r.Release, &r.Action, &r.PlanHash,
			&r.Actor, &changed, &r.Error, &r.Note, &r.Revision, &r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		r.Changed = changed == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// Run is one row with its output. The output is why the log is worth keeping --
// a failed apply's helmfile stderr lives nowhere else -- so it is fetched per
// row rather than never.
func (s *Store) Run(ctx context.Context, id int64) (Run, error) {
	var r Run
	var changed int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, namespace, release, action, plan_hash, actor, changed, error, output, note, revision, started_at, ended_at
		 FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.Namespace, &r.Release, &r.Action, &r.PlanHash,
			&r.Actor, &changed, &r.Error, &r.Output, &r.Note, &r.Revision, &r.StartedAt, &r.EndedAt)
	if err == sql.ErrNoRows {
		return Run{}, fmt.Errorf("no run %d", id)
	}
	r.Changed = changed == 1
	return r, err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
