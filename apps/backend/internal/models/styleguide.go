package models

// Style guide statuses: generation runs in the background after the upload.
const (
	StyleguideGenerating = "generating"
	StyleguideReady      = "ready"
	StyleguideFailed     = "failed"
)

// Styleguide is a markdown design guide written by the styleguide-generator
// agent from sample images and/or pasted text. A chat thread picks one with its first message; its agents
// get it in their system prompt and at /styleguide/STYLEGUIDE.md.
type Styleguide struct {
	Base
	OwnerID string `gorm:"index;size:64"`
	Name    string `gorm:"size:200"`
	// Notes are the user's hints for the generator ("use the blue as accent").
	Notes string `gorm:"type:text"`
	// Images are the sample images as wcblob:// refs, in upload order.
	Images []string `gorm:"serializer:json"`
	// Text is pasted source material (brand guidelines, tokens, a description,
	// an existing guide); a guide can come from text alone, images alone or both.
	Text     string `gorm:"type:text"`
	Markdown string `gorm:"type:text"`
	Status   string `gorm:"size:16"`
	Error    string `gorm:"type:text"`
}

func (Styleguide) TableName() string { return "styleguides" }
