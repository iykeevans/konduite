package messages

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

const parentFKey = "messages_parent_fkey"

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Append(ctx context.Context, n NewMessage) (Message, bool, error) {
	var (
		m       Message
		created bool
	)
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, created, err = AppendTx(ctx, tx, n)
		return err
	})
	return m, created, err
}

func (r postgresRepository) Post(ctx context.Context, in Posting) (Message, bool, error) {
	var (
		m       Message
		created bool
	)
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, created, err = AppendTx(ctx, tx, in.Message)
		if err != nil || !created {
			return err
		}
		q := store.New(tx)
		if _, err := q.AddConversationParticipant(ctx, store.AddConversationParticipantParams{
			ConversationID:  m.ConversationID,
			ParticipantType: string(m.Sender.Kind),
			ParticipantID:   m.Sender.ID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = events.Enqueue(ctx, q, CreatedEvent(m, in.WorkspaceID, in.ProjectID))
		return err
	})
	if err != nil {
		return Message{}, false, err
	}
	return m, created, nil
}

func (r postgresRepository) List(ctx context.Context, f ListFilter) ([]Message, error) {
	rows, err := store.New(r.pool).ListMessagesAfter(ctx, store.ListMessagesAfterParams{
		ConversationID: f.ConversationID, AfterSeq: f.AfterSeq, RowLimit: int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func (r postgresRepository) Get(ctx context.Context, id uuid.UUID) (Message, error) {
	row, err := store.New(r.pool).GetMessage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return fromRow(row), nil
}

// AppendTx stores n in tx as the conversation's next message, so the caller
// can enqueue its outbox event in the same transaction. If the sender already
// sent a message with n's idempotency key, it returns that message with
// created false, writes nothing and does not use up a sequence number.
//
// The conversation row stays locked until tx ends, which is what keeps
// sequence numbers gapless and ordered: commit promptly. tx must be READ
// COMMITTED (the default) so a retry that raced the original can read it.
//
// A failed write is rolled back to a savepoint, so tx stays usable after
// ErrConversationNotFound or ErrParentNotFound.
func AppendTx(ctx context.Context, tx pgx.Tx, n NewMessage) (Message, bool, error) {
	if m, ok, err := byIdempotencyKey(ctx, store.New(tx), n); err != nil || ok {
		return m, false, err
	}

	var (
		row      store.Message
		inserted bool
	)
	err := db.WithTx(ctx, tx, func(ctx context.Context, sp pgx.Tx) error {
		q := store.New(sp)
		seq, err := q.NextConversationSeq(ctx, n.ConversationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConversationNotFound
		}
		if err != nil {
			return err
		}
		metadata := n.Metadata
		if metadata == nil {
			metadata = []byte("{}")
		}
		row, err = q.InsertMessage(ctx, store.InsertMessageParams{
			ConversationID: n.ConversationID,
			SequenceNumber: seq,
			SenderType:     string(n.Sender.Kind),
			SenderID:       n.Sender.ID,
			IdempotencyKey: n.IdempotencyKey,
			ParentID:       n.ParentID,
			MessageType:    string(n.Type),
			Content:        n.Content,
			Metadata:       metadata,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent retry committed first. Roll the sequence bump back.
			return errDuplicate
		}
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.ConstraintName == parentFKey {
			return ErrParentNotFound
		}
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if errors.Is(err, errDuplicate) {
		m, ok, err := byIdempotencyKey(ctx, store.New(tx), n)
		if err == nil && !ok {
			err = errors.New("messages: duplicate message vanished")
		}
		return m, false, err
	}
	if err != nil {
		return Message{}, false, err
	}
	return fromRow(row), inserted, nil
}

var errDuplicate = errors.New("messages: duplicate idempotency key")

func byIdempotencyKey(ctx context.Context, q *store.Queries, n NewMessage) (Message, bool, error) {
	row, err := q.GetMessageByIdempotencyKey(ctx, store.GetMessageByIdempotencyKeyParams{
		ConversationID: n.ConversationID, SenderID: n.Sender.ID, IdempotencyKey: n.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	return fromRow(row), true, nil
}

func fromRow(r store.Message) Message {
	return Message{
		ID:             r.ID,
		ConversationID: r.ConversationID,
		SequenceNumber: r.SequenceNumber,
		Sender:         Sender{Kind: conversations.ParticipantKind(r.SenderType), ID: r.SenderID},
		IdempotencyKey: r.IdempotencyKey,
		ParentID:       r.ParentID,
		Type:           Type(r.MessageType),
		Content:        r.Content,
		Metadata:       r.Metadata,
		CreatedAt:      r.CreatedAt,
	}
}
