package database

import (
	"context"
	"database/sql"
	"sync"
)

// Reuse query plans across the read pool. sql.Stmt maintains one prepared
// statement per underlying connection. Bound the cache for dynamic IN queries.
type readPool struct {
	*sql.DB
	mu         sync.RWMutex
	statements map[string]*sql.Stmt
}

func (p *readPool) statement(ctx context.Context, query string) *sql.Stmt {
	p.mu.RLock()
	statement := p.statements[query]
	p.mu.RUnlock()
	if statement != nil {
		return statement
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if statement = p.statements[query]; statement != nil {
		return statement
	}
	if len(p.statements) >= 256 {
		return nil
	}
	statement, err := p.DB.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}
	p.statements[query] = statement
	return statement
}
func (p *readPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if statement := p.statement(ctx, query); statement != nil {
		return statement.QueryContext(ctx, args...)
	}
	return p.DB.QueryContext(ctx, query, args...)
}
func (p *readPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if statement := p.statement(ctx, query); statement != nil {
		return statement.QueryRowContext(ctx, args...)
	}
	return p.DB.QueryRowContext(ctx, query, args...)
}
func (p *readPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, statement := range p.statements {
		statement.Close()
	}
	clear(p.statements)
	return p.DB.Close()
}
