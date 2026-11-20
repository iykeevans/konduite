package conversations

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Create(ctx context.Context, n NewConversation) (Conversation, error) {
	var c Conversation
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		if n.ProjectID != nil {
			// FOR KEY SHARE keeps the project from being deleted before commit.
			_, err := q.LockProjectInWorkspace(ctx, store.LockProjectInWorkspaceParams{ID: *n.ProjectID, WorkspaceID: n.WorkspaceID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProjectNotFound
			}
			if err != nil {
				return err
			}
		}
		row, err := q.InsertConversation(ctx, store.InsertConversationParams{
			WorkspaceID: n.WorkspaceID, ProjectID: n.ProjectID, Title: n.Title, CreatedBy: n.CreatedBy,
		})
		if err != nil {
			return err
		}
		p, err := q.InsertConversationParticipant(ctx, store.InsertConversationParticipantParams{
			ConversationID: row.ID, ParticipantType: string(KindUser), ParticipantID: n.CreatedBy,
		})
		if err != nil {
			return err
		}
		c = fromRow(row)
		c.Participants = []Participant{fromParticipantRow(p)}
		return nil
	})
	if err != nil {
		return Conversation{}, err
	}
	return c, nil
}

func (r postgresRepository) Get(ctx context.Context, id uuid.UUID) (Conversation, error) {
	q := store.New(r.pool)
	row, err := q.GetConversation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	rows, err := q.ListConversationParticipants(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	c := fromRow(row)
	c.Participants = make([]Participant, 0, len(rows))
	for _, p := range rows {
		c.Participants = append(c.Participants, fromParticipantRow(p))
	}
	return c, nil
}

func (r postgresRepository) List(ctx context.Context, f ListFilter) ([]Conversation, error) {
	params := store.ListConversationsParams{WorkspaceID: f.WorkspaceID, ProjectID: f.ProjectID, ViewerID: f.ViewerID, RowLimit: int32(f.Limit)} //nolint:gosec // Limit is at most 101, set by the service.
	if f.After != nil {
		params.AfterCreatedAt = &f.After.CreatedAt
		params.AfterID = &f.After.ID
	}
	rows, err := store.New(r.pool).ListConversations(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func fromRow(r store.Conversation) Conversation {
	return Conversation{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID, Title: r.Title, CreatedBy: r.CreatedBy,
		LastSeq: r.LastSeq, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func fromParticipantRow(p store.ConversationParticipant) Participant {
	return Participant{Kind: ParticipantKind(p.ParticipantType), ID: p.ParticipantID, JoinedAt: p.JoinedAt}
}
