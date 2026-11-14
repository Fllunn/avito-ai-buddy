package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const messageColumns = `id,sequence,user_id,client_message_id::text,role,kind,text,intent,form_required,created_at,slots,metadata,ARRAY(SELECT l.run_id::text FROM execution_links l WHERE l.kind='message' AND l.object_id=messages.id::text AND l.user_id=messages.user_id ORDER BY l.run_id)`

func scanMessage(row pgx.Row) (actions.Message, error) {
	var m actions.Message
	var slots []byte
	err := row.Scan(&m.ID, &m.Sequence, &m.UserID, &m.ClientID, &m.Role, &m.Kind, &m.Text, &m.Intent, &m.FormRequired, &m.CreatedAt, &slots, &m.Metadata, &m.ExecutionIDs)
	if err == nil {
		err = json.Unmarshal(slots, &m.Slots)
	}
	return m, err
}
func (s *Store) AcceptMessage(ctx context.Context, user, client, kind, intent string, text safety.Text, now time.Time) (actions.Message, bool, error) {
	return s.acceptMessage(ctx, user, client, kind, intent, text, actions.Slots{}, now)
}
func (s *Store) AcceptAction(ctx context.Context, user, client, intent string, slots actions.Slots, now time.Time) (actions.Message, bool, error) {
	return s.acceptMessage(ctx, user, client, "button", intent, safety.Text{}, slots, now)
}
func (s *Store) acceptMessage(ctx context.Context, user, client, kind, intent string, text safety.Text, slots actions.Slots, now time.Time) (actions.Message, bool, error) {
	formRequired := text.UseForm() || slots.RequiresForm()
	values, err := slots.Values()
	if err != nil {
		return actions.Message{}, false, err
	}
	if kind == "text" && strings.TrimSpace(text.String()) == "" {
		return actions.Message{}, false, actions.ErrInvalid
	}

	if !actions.ValidID(client) || kind != "text" && kind != "button" {
		return actions.Message{}, false, actions.ErrInvalid
	}
	if kind == "text" && intent != "" || kind == "button" && !actions.KnownIntent(intent) {
		return actions.Message{}, false, actions.ErrInvalid
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return actions.Message{}, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `INSERT INTO conversations(user_id) VALUES($1) ON CONFLICT DO NOTHING`, user); err != nil {
		return actions.Message{}, false, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, `SELECT next_sequence FROM conversations WHERE user_id=$1 FOR UPDATE`, user).Scan(&sequence); err != nil {
		return actions.Message{}, false, err
	}
	existing, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE user_id=$1 AND client_message_id=$2`, user, client))
	if err == nil {
		if existing.Kind != kind || existing.Text != text.String() || existing.Intent != intent || existing.FormRequired != formRequired || !reflect.DeepEqual(existing.Slots, values) {
			return actions.Message{}, false, actions.ErrMessageConflict
		}
		return existing, false, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return actions.Message{}, false, err
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return actions.Message{}, false, err
	}
	message, err := scanMessage(tx.QueryRow(ctx, `INSERT INTO messages(user_id,sequence,client_message_id,role,kind,text,intent,form_required,created_at,slots,operational_created_at) VALUES($1,$2,$3,'user',$4,$5,$6,$7,$8,$9,$10) RETURNING `+messageColumns, user, sequence, client, kind, text.String(), intent, formRequired, now, payload, s.OperationalClock.Now()))
	if err != nil {
		return actions.Message{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE conversations SET next_sequence=next_sequence+1 WHERE user_id=$1`, user); err != nil {
		return actions.Message{}, false, err
	}
	jobID := actions.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,user_id,message_id,kind,state,available_at,created_at) VALUES($1,$2,$3,'message','queued',$4,$4)`, jobID, user, message.ID, s.OperationalClock.Now()); err != nil {
		return actions.Message{}, false, err
	}
	if err := recordEvent(ctx, tx, user, "message:"+strconv.FormatInt(message.ID, 10), "chat_message_sent", kind, "accepted", 0, now); err != nil {
		return actions.Message{}, false, err
	}
	if err = s.executionForMessage(ctx, tx, message, jobID); err != nil {
		return actions.Message{}, false, err
	}
	return message, true, tx.Commit(ctx)
}
func (s *Store) Messages(ctx context.Context, user string, after int64) ([]actions.Message, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+messageColumns+` FROM messages WHERE user_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 100`, user, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []actions.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
