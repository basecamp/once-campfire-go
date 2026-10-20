package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

// Submitted bodies can be parsed and, without mentions, reduced to text before
// taking the sole writer. Mention names and attachment fallback still come from
// the mutation's transaction; this preparation contains no database observations.
type messageSearchBody struct {
	plain   string
	doc     *richtext.Document
	targets map[string]int64
	ids     map[int64]bool
	errors  map[string]error
}

func prepareMessageSearchBody(body string) messageSearchBody {
	doc := richtext.Prepare(body)
	tokens := doc.PlainAttachables()
	if len(tokens) == 0 {
		plain, _ := doc.PlainText(richtext.Context{})
		return messageSearchBody{plain: plain}
	}
	prepared := messageSearchBody{doc: &doc, targets: map[string]int64{}, ids: map[int64]bool{}, errors: map[string]error{}}
	for _, token := range tokens {
		id, err := rails.UnverifiedUserID(token)
		prepared.targets[token] = id
		prepared.errors[token] = err
		if id != 0 {
			prepared.ids[id] = true
		}
	}
	return prepared
}

func messageSearchText(ctx context.Context, tx *sql.Tx, messageID int64, body messageSearchBody) (string, error) {
	// Content failures deliberately retain the reference's empty-text fallback.
	// SQL/cancellation failures are different: the command must roll back.
	mentions := map[int64]*richtext.Mention{}
	if len(body.ids) > 0 {
		rows, err := tx.QueryContext(ctx, "SELECT id,name FROM users WHERE id IN (SELECT value FROM json_each(?))", displayIDs(body.ids))
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var mention richtext.Mention
			if err := rows.Scan(&mention.ID, &mention.Name); err != nil {
				rows.Close()
				return "", err
			}
			mentions[mention.ID] = &mention
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	plain := body.plain
	if body.doc != nil {
		plain, _ = body.doc.PlainText(richtext.Context{Resolve: func(token string, _ bool) (*richtext.Mention, error) {
			return mentions[body.targets[token]], body.errors[token]
		}})
	}
	if strings.TrimSpace(plain) != "" {
		return plain, nil
	}
	var filename string
	err := tx.QueryRowContext(ctx, "SELECT b.filename FROM active_storage_attachments a JOIN active_storage_blobs b ON b.id=a.blob_id WHERE a.record_type='Message' AND a.record_id=? AND a.name='attachment' ORDER BY a.id LIMIT 1", messageID).Scan(&filename)
	if errors.Is(err, sql.ErrNoRows) {
		return plain, nil
	}
	if err != nil {
		return "", err
	}
	return rails.Filename(filename), nil
}
