package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type InviteToken struct{ ent.Schema }

func (InviteToken) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("token_hash").Sensitive().NotEmpty().MaxLen(128), // sha256 hex of token
		field.String("email").NotEmpty().MaxLen(254),
		field.UUID("invited_by", uuid.UUID{}),
		field.Time("expires_at"),
		field.Time("accepted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (InviteToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("token_hash").Unique(),
		index.Fields("email"),
	}
}
