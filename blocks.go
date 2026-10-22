package claudecode

import (
	"errors"
	"fmt"
)

// Content block types accepted by QueryBlocks and PromptBlocks.
const (
	// PromptBlockText is a text block.
	PromptBlockText = "text"
	// PromptBlockImage is an image block; its Source carries the bytes.
	PromptBlockImage = "image"
)

// imageSourceBase64 is the only image source kind the CLI's stream-json input
// is documented to take from this package: inline base64 data.
const imageSourceBase64 = "base64"

// PromptBlock is one block of a multi-block user turn, in the Anthropic
// message-content shape the CLI accepts on stream-json input. Build one with
// PromptText or PromptImage.
type PromptBlock struct {
	Type   string             `json:"type"`
	Text   string             `json:"text,omitempty"`
	Source *PromptImageSource `json:"source,omitempty"`
}

// PromptImageSource is the source of an image block.
type PromptImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// PromptText returns a text block.
func PromptText(text string) PromptBlock {
	return PromptBlock{Type: PromptBlockText, Text: text}
}

// PromptImage returns an image block holding base64Data, standard-base64
// encoded image bytes with no "data:" prefix, of mediaType (for example
// "image/png"). The Anthropic API accepts image/jpeg, image/png, image/gif and
// image/webp; this package does not restrict the type.
func PromptImage(mediaType, base64Data string) PromptBlock {
	return PromptBlock{
		Type:   PromptBlockImage,
		Source: &PromptImageSource{Type: imageSourceBase64, MediaType: mediaType, Data: base64Data},
	}
}

// validateBlocks rejects a turn the CLI could not make sense of before
// anything is written to it: no blocks, an unknown block type, or an image
// block without a media type or data.
func validateBlocks(blocks []PromptBlock) error {
	if len(blocks) == 0 {
		return errors.New("claudecode: send prompt: no content blocks")
	}

	for i, b := range blocks {
		switch b.Type {
		case PromptBlockText:
		case PromptBlockImage:
			if b.Source == nil || b.Source.MediaType == "" || b.Source.Data == "" {
				return fmt.Errorf("claudecode: send prompt: image block %d needs a media type and data", i)
			}
		default:
			return fmt.Errorf("claudecode: send prompt: block %d has unknown type %q", i, b.Type)
		}
	}

	return nil
}
