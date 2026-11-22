package memberapi

import (
	"net/url"
)

// previewNode projects Telegram rich-message nodes onto the fields rendered by
// PublicationPreview. Never pass an untyped Telegram object to a member client.
type previewObject map[string]any

func previewNodes(value any, depth int, project func(any, int) any) []any {
	items, _ := value.([]any)
	result := make([]any, 0, len(items))
	for _, item := range items {
		if node := project(item, depth+1); node != nil {
			result = append(result, node)
		}
	}
	return result
}

func previewURL(value any) string {
	s, _ := value.(string)
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return s
}

func previewText(value any, depth int) any {
	if depth > 40 {
		return nil
	}
	switch node := value.(type) {
	case string:
		return node
	case []any:
		return previewNodes(node, depth, previewText)
	case map[string]any:
		var inner any
		for _, field := range []string{"text", "items", "alternative_text", "expression"} {
			if node[field] != nil {
				inner = previewText(node[field], depth+1)
				break
			}
		}
		kind, _ := node["type"].(string)
		switch kind {
		case "bold", "italic", "underline", "strikethrough", "code", "monospace", "spoiler", "subscript", "superscript", "marked":
			return previewObject{"type": kind, "text": inner}
		case "url", "link":
			return previewObject{"type": kind, "text": inner, "url": previewURL(node["url"])}
		default:
			return inner
		}
	}
	return nil
}

func previewBlock(value any, depth int) any {
	src, ok := value.(map[string]any)
	if !ok || depth > 20 {
		return nil
	}
	kind, _ := src["type"].(string)
	dst := previewObject{"type": kind}
	setText := func(key string) { dst[key] = previewText(src[key], 0) }
	children := func() { dst["blocks"] = previewNodes(src["blocks"], depth, previewBlock) }
	switch kind {
	case "heading":
		size, _ := src["size"].(float64)
		dst["size"] = size
		setText("text")
	case "paragraph", "footer", "pre":
		setText("text")
	case "divider", "anchor":
	case "blockquote", "quote", "pullquote", "expandable_blockquote":
		setText("text")
		setText("credit")
		children()
	case "collage", "slideshow":
		setText("caption")
		children()
	case "details":
		dst["is_open"], _ = src["is_open"].(bool)
		setText("summary")
		children()
	case "list":
		dst["items"] = previewNodes(src["items"], depth, func(v any, _ int) any {
			item, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			return previewObject{"label": previewText(item["label"], 0), "blocks": previewNodes(item["blocks"], depth, previewBlock)}
		})
	case "table":
		dst["cells"] = previewNodes(src["cells"], depth, func(v any, _ int) any {
			return previewNodes(v, depth, func(v any, _ int) any {
				cell, ok := v.(map[string]any)
				if !ok {
					return nil
				}
				return previewObject{"text": previewText(cell["text"], 0)}
			})
		})
	case "photo":
		dst["has_spoiler"], _ = src["has_spoiler"].(bool)
		setText("caption")
	case "buttons":
		dst["buttons"] = previewNodes(src["buttons"], depth, func(v any, _ int) any {
			button, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			out := previewObject{"text": previewText(button["text"], 0), "url": previewURL(button["url"])}
			if button["style"] == "success" {
				out["style"] = "success"
			}
			return out
		})
	default:
		dst["type"] = "unsupported"
		setText("text")
		children()
	}
	return dst
}

func previewEntity(value any, _ int) any {
	src, _ := value.(map[string]any)
	kind, _ := src["type"].(string)
	switch kind {
	case "bold", "italic", "underline", "strikethrough", "code", "pre", "spoiler", "blockquote", "expandable_blockquote", "url", "mention", "text_link":
	default:
		return nil
	}
	offset, okOffset := src["offset"].(float64)
	length, okLength := src["length"].(float64)
	if !okOffset || !okLength {
		return nil
	}
	out := previewObject{"type": kind, "offset": offset, "length": length}
	if kind == "text_link" {
		out["url"] = previewURL(src["url"])
	}
	return out
}

type announcementPreview struct {
	ID          int64         `json:"id"`
	Text        string        `json:"text"`
	Entities    []any         `json:"entities"`
	RichMessage previewObject `json:"rich_message"`
	Media       []struct {
		Position int `json:"position"`
	} `json:"media"`
}

func (p *announcementPreview) project() {
	p.Entities = previewNodes(p.Entities, 0, previewEntity)
	if p.RichMessage != nil {
		p.RichMessage = previewObject{"blocks": previewNodes(p.RichMessage["blocks"], 0, previewBlock)}
	}
}
