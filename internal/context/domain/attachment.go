package domain

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxAttachmentBytes is REQ-CTX-005's limit: past it an attachment is truncated.
const MaxAttachmentBytes = 256 << 10

// Kind is what an attachment refers to (API §4 Message, `attachments[].kind`).
type Kind string

// The attachment kinds.
const (
	KindFile  Kind = "file"
	KindDir   Kind = "dir"
	KindBlock Kind = "block"
	KindStdin Kind = "stdin"
)

// Ref is an attachment as a message names it: a path for a file or a directory, relative to
// the thread's cwd, or a block id.
type Ref struct {
	Kind Kind
	Ref  string
}

// Attachment is an attachment's content, ready for the prompt, and what `attachments_json`
// records of it.
type Attachment struct {
	Kind    Kind
	Ref     string
	Content string
	// Bytes is the attachment's whole size, read or not.
	Bytes int64
	// TruncatedBytes is how many of those bytes the prompt leaves out.
	TruncatedBytes int64
	// Binary is set when the content was not text and none of it is included.
	Binary bool
}

// NewAttachment builds an attachment from the first bytes of its content and its whole size,
// which may be larger than what was read. It keeps at most MaxAttachmentBytes, cut on a rune
// boundary, and counts the rest as omitted (REQ-CTX-005). Content with a NUL byte is binary
// and none of it is kept.
func NewAttachment(kind Kind, ref string, content []byte, total int64) Attachment {
	total = max(total, int64(len(content)))
	a := Attachment{Kind: kind, Ref: ref, Bytes: total}
	if bytes.IndexByte(content, 0) >= 0 {
		a.Binary = true
		a.TruncatedBytes = total
		return a
	}
	kept := cut(content, MaxAttachmentBytes)
	a.Content = string(kept)
	a.TruncatedBytes = total - int64(len(kept))
	return a
}

// cut returns at most limit bytes of b, never ending inside a UTF-8 sequence.
func cut(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	b = b[:limit]
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// Render is the attachment as the user message carries it.
func (a Attachment) Render() string {
	var s strings.Builder
	fmt.Fprintf(&s, "<attachment kind=%q ref=%q bytes=\"%d\">\n", a.Kind, a.Ref, a.Bytes)
	switch {
	case a.Binary:
		fmt.Fprintf(&s, "[binary content: %d bytes omitted]\n", a.TruncatedBytes)
	default:
		s.WriteString(a.Content)
		if !strings.HasSuffix(a.Content, "\n") {
			s.WriteString("\n")
		}
		if a.TruncatedBytes > 0 {
			fmt.Fprintf(&s, "[truncated: %d bytes omitted]\n", a.TruncatedBytes)
		}
	}
	s.WriteString("</attachment>")
	return s.String()
}
