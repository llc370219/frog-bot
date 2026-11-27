package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// Summarizer generates a concise 1-sentence factual summary (typically using a cheap light model).
type Summarizer func(ctx context.Context, text string) (string, error)

// HistoryItem represents one archived turn/message stored in SQLite FTS5 (trigram).
type HistoryItem struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Summary   string    `json:"summary,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SearchResult is returned by session_search(query, limit).
type SearchResult struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Summary   string    `json:"summary"`
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"created_at"`
}

// HistoryStore manages <botDir>/history.db using pure-Go SQLite FTS5 with trigram tokenizer
// for zero-CGO native Chinese substring full-text retrieval.
type HistoryStore struct {
	path       string
	db         *sql.DB
	summarizer Summarizer
	mu         sync.Mutex
	wg         sync.WaitGroup
}

func OpenHistoryStore(dbPath string, sum Summarizer) (*HistoryStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open history.db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer safety
	_ = os.Chmod(dbPath, 0o600)

	ddl := `
CREATE TABLE IF NOT EXISTS history_items (
	item_id     TEXT PRIMARY KEY,
	role        TEXT NOT NULL,
	content     TEXT NOT NULL,
	summary     TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	archived_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_created ON history_items(created_at DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
	item_id UNINDEXED,
	role UNINDEXED,
	content,
	summary,
	tokenize='trigram'
);
`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("init history_fts schema: %w", err)
	}
	return &HistoryStore{path: dbPath, db: db, summarizer: sum}, nil
}

func (h *HistoryStore) Close() error {
	h.wg.Wait()
	if h.db != nil {
		return h.db.Close()
	}
	return nil
}

// Archive persists a single item into history_items + history_fts (idempotent on item_id).
func (h *HistoryStore) Archive(ctx context.Context, item HistoryItem) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Content) == "" {
		return nil
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.Summary == "" {
		item.Summary = fallbackSummary(item.Content, 120)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	_ = tx.QueryRowContext(ctx, `SELECT 1 FROM history_items WHERE item_id = ?`, item.ID).Scan(&exists)
	if exists == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE history_items SET content=?, summary=? WHERE item_id=?`, item.Content, item.Summary, item.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_fts WHERE item_id=?`, item.ID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO history_items(item_id, role, content, summary, created_at, archived_at) VALUES (?,?,?,?,?,?)`,
			item.ID, item.Role, item.Content, item.Summary, item.CreatedAt.Unix(), time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO history_fts(item_id, role, content, summary) VALUES (?,?,?,?)`,
		item.ID, item.Role, item.Content, item.Summary); err != nil {
		return err
	}
	return tx.Commit()
}

// ArchiveAsync archives evicted items immediately with deterministic fallback summaries,
// then upgrades summaries asynchronously using the configured cheap light model.
func (h *HistoryStore) ArchiveAsync(ctx context.Context, items []HistoryItem) {
	for _, it := range items {
		if err := h.Archive(ctx, it); err != nil {
			continue
		}
		if h.summarizer != nil && it.Summary == "" && utf8.RuneCountInString(it.Content) > 80 {
			item := it
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer cancel()
				if sum, err := h.summarizer(sctx, item.Content); err == nil && strings.TrimSpace(sum) != "" {
					item.Summary = truncateRunes(strings.TrimSpace(sum), 160)
					_ = h.Archive(sctx, item)
				}
			}()
		}
	}
}

// Search performs native Chinese substring retrieval via SQLite FTS5 trigram
// (with automatic LIKE fallback for 1-2 character queries where trigrams don't apply).
func (h *HistoryStore) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var results []SearchResult
	// SQLite trigram tokenizer indexes >= 3-rune tokens.
	if utf8.RuneCountInString(query) >= 3 {
		quoted := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
		rows, err := h.db.QueryContext(ctx, `
			SELECT i.item_id, i.role, i.summary, i.content, i.created_at
			FROM history_fts f
			JOIN history_items i ON i.item_id = f.item_id
			WHERE history_fts MATCH ?
			ORDER BY bm25(history_fts), i.created_at DESC
			LIMIT ?`, quoted, limit)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var r SearchResult
				var content string
				var ts int64
				if rows.Scan(&r.ID, &r.Role, &r.Summary, &content, &ts) == nil {
					r.CreatedAt = time.Unix(ts, 0)
					r.Snippet = extractSnippet(content, query, 220)
					results = append(results, r)
				}
			}
		}
	}
	if len(results) > 0 {
		return results, nil
	}

	// Fallback for 1-2 char queries or punctuation-heavy terms.
	like := "%" + query + "%"
	rows, err := h.db.QueryContext(ctx, `
		SELECT item_id, role, summary, content, created_at
		FROM history_items
		WHERE content LIKE ? OR summary LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r SearchResult
		var content string
		var ts int64
		if rows.Scan(&r.ID, &r.Role, &r.Summary, &content, &ts) == nil {
			r.CreatedAt = time.Unix(ts, 0)
			r.Snippet = extractSnippet(content, query, 220)
			results = append(results, r)
		}
	}
	return results, nil
}

// Count returns total archived records in history.db.
func (h *HistoryStore) Count(ctx context.Context) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int
	_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM history_items`).Scan(&n)
	return n
}

// SlidingWindow splits history into (keep, evicted) keeping the most recent maxRounds
// user/assistant pairs (default 10 rounds = 20 messages).
func SlidingWindow[T any](items []T, maxRounds int) (keep []T, evicted []T) {
	if maxRounds <= 0 {
		maxRounds = 10
	}
	maxMsgs := maxRounds * 2
	if len(items) <= maxMsgs {
		return items, nil
	}
	cut := len(items) - maxMsgs
	return items[cut:], items[:cut]
}

func fallbackSummary(text string, maxRunes int) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return truncateRunes(clean, maxRunes)
}

func extractSnippet(content, query string, maxRunes int) string {
	content = strings.TrimSpace(content)
	idx := strings.Index(strings.ToLower(content), strings.ToLower(query))
	if idx < 0 {
		return truncateRunes(content, maxRunes)
	}
	start := idx - 60
	if start < 0 {
		start = 0
	}
	// Align to valid UTF-8 boundary
	for start > 0 && !utf8.RuneStart(content[start]) {
		start--
	}
	sub := content[start:]
	if start > 0 {
		return "…" + truncateRunes(sub, maxRunes)
	}
	return truncateRunes(sub, maxRunes)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…"
}
