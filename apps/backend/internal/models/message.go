package models

// Message roles.
const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
	MessageRoleSystem    = "system"
)

// Message is one entry of a conversation thread.
type Message struct {
	Base
	ThreadID string `gorm:"index;not null" json:"thread_id" crud:"create,filter" validate:"required"`
	Role     string `gorm:"not null" json:"role" crud:"create,filter,sort" validate:"oneof=user assistant system"`
	Body     string `gorm:"not null" json:"body" crud:"create,update" validate:"required,max=20000"`
	// OwnerID is server-set (no crud tag); auth integration fills it later.
	OwnerID string `gorm:"index" json:"owner_id"`
}
