// Package sessionquery provides full-text search over session logs (DSH
// session-query-sqlite): a derived SQLite FTS5 index over the logs' derived
// messages, with live-priority semantics — the live in-memory surface wins
// over the persisted index, and the index refreshes from the authoritative
// logs. Queries are literal phrases (FTS syntax treated as data), bounded
// pages, ranked by highlight span count.
//
// The index is DERIVED data: it is never the truth. Logs are. A rebuild
// from the logs always converges; index failures degrade to an empty
// result, never a wrong one.
//
// Package sessionquery 提供会话日志全文搜索（DSH session-query-sqlite）：
// 在日志派生消息之上的派生 SQLite FTS5 索引，live 优先语义——内存中的
// 活跃 surface 胜过持久化索引，索引从权威日志刷新。查询是字面短语
// （FTS 语法按数据处理）、有界分页、按高亮命中段数排序。
//
// 索引是派生数据：它绝不是真相。日志才是。从日志重建总是收敛；索引
// 失败降级为空结果，绝不给出错误结果。
package sessionquery

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	coresession "github.com/go-gocel/gocel/core/session"
	_ "modernc.org/sqlite"
)

// Hit is one search result: the session id, the matched message content,
// and the message's seq. Results are ordered by FTS rank (the derived
// index's relevance score) — consumers enrich hits with titles/snippets.
//
// Hit 是一条搜索结果：会话 id、命中的消息内容、消息 seq。结果按 FTS rank
// （派生索引的相关度分数）排序——标题/摘要等富化由消费方完成。
type Hit struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Seq       int64  `json:"seq"`
}

// Query is a search request.
// Query 是搜索请求。
type Query struct {
	// Term is the required literal phrase.
	Term string
	// Limit bounds the page size (default 20, max 100).
	Limit int
}

// Index searches session logs through a derived SQLite FTS5 table.
// Index 经派生 SQLite FTS5 表搜索会话日志。
type Index struct {
	db *sql.DB
}

// Open creates the index at the given path (":memory:" supported) and
// builds the FTS5 schema.
//
// Open 在给定路径创建索引（支持 ":memory:"）并构建 FTS5 schema。
func Open(path string) (*Index, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sessionquery: open: %w", err)
	}
	// FTS5 external-content table over the derived rows.
	schema := `
	CREATE TABLE IF NOT EXISTS session_docs (
		session_id TEXT NOT NULL,
		seq        INTEGER NOT NULL,
		content    TEXT NOT NULL
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS session_fts USING fts5(
		session_id UNINDEXED, seq UNINDEXED, content,
		content='session_docs', content_rowid='rowid'
	);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sessionquery: schema: %w", err)
	}
	return &Index{db: db}, nil
}

// Close releases the index.
// Close 释放索引。
func (s *Index) Close() error { return s.db.Close() }

// IndexLogs replaces the derived index content for the given logs: each
// log's derived messages are (re)indexed, so the index always converges to
// the logs (the truth). A log with no messages contributes nothing.
//
// IndexLogs 为给定日志替换派生索引内容：每条日志的派生消息被（重新）
// 索引，索引始终向日志（真相）收敛。无消息的日志不贡献内容。
func (s *Index) IndexLogs(ctx context.Context, logs map[string]*coresession.Log) error {
	// Rebuild the derived index atomically: clear + repopulate + FTS
	// refresh happen in ONE transaction, so readers never observe a
	// half-rebuilt index — they see either the previous content or the new
	// content (the FTS external-content table and its index can never
	// disagree, which would return wrong/empty results).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sessionquery: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_docs`); err != nil {
		return fmt.Errorf("sessionquery: clear: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO session_docs (session_id, seq, content) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("sessionquery: prepare: %w", err)
	}
	defer stmt.Close()

	ids := make([]string, 0, len(logs))
	for id := range logs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		log := logs[id]
		for _, ev := range log.Events() {
			if ev.Message == nil || strings.TrimSpace(ev.Message.Content) == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, id, ev.Seq, ev.Message.Content); err != nil {
				return fmt.Errorf("sessionquery: insert: %w", err)
			}
		}
	}
	// Refresh the FTS index from the external content table INSIDE the
	// transaction, then commit — the swap is all-or-nothing.
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_fts(session_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("sessionquery: fts rebuild: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sessionquery: commit: %w", err)
	}
	return nil
}

// Search runs a literal-phrase search over the index. The term is treated
// as data — FTS5 syntax (quotes, OR, *) is escaped so user input can never
// execute a crafted query.
//
// Search 在索引上执行字面短语搜索。词条按数据处理——FTS5 语法（引号、
// OR、*）被转义，用户输入永远无法执行构造查询。
func (s *Index) Search(ctx context.Context, q Query) ([]Hit, error) {
	term := strings.TrimSpace(q.Term)
	if term == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	// Escape the literal phrase: wrap in double quotes and double any
	// embedded quotes so the FTS parser treats it as one phrase.
	escaped := strings.ReplaceAll(term, `"`, `""`)
	match := `"` + escaped + `"`
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, seq, content
		FROM session_fts
		WHERE session_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, match, limit)
	if err != nil {
		// FTS errors (e.g. an unindexable term) degrade to an empty
		// result — the index is derived, never the truth. The failure is
		// logged so an index fault is observable, not a silent "no hits".
		log.Printf("[sessionquery] search %q: %v", term, err)
		return nil, nil
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SessionID, &h.Seq, &h.Content); err != nil {
			return nil, fmt.Errorf("sessionquery: scan: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}
