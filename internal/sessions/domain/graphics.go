package domain

import "bytes"

// GraphicsPlaceholder is what a client without the `graphics` capability sees where a pane
// displays a Kitty graphics image (REQ-PLG-003, delta `2026-10-pane-graphics`).
const GraphicsPlaceholder = "[image]"

// maxGraphicsControl bounds the control data of one graphics escape a filter keeps: the keys
// it reads (`a`, `m`, `U`) come first in practice, and nothing past this is needed.
const maxGraphicsControl = 256

type graphicsState uint8

const (
	gfxGround   graphicsState = iota
	gfxEsc                    // after ESC
	gfxApc                    // after ESC _
	gfxControl                // inside ESC _ G, before ';'
	gfxPayload                // after ';', until ESC
	gfxEndOfApc               // ESC inside a graphics escape
)

// GraphicsFilter takes the Kitty graphics escapes (`ESC _ G … ESC \`) out of a pane's output
// stream, for a client that did not advertise `graphics`. An image the escape displays — `a=T`
// once its last chunk is in, or `a=p` — becomes GraphicsPlaceholder; transmissions, queries and
// deletions leave nothing. A placement with `U=1` leaves nothing either: its Unicode placeholder
// cells are ordinary text that follows. Every other byte passes unchanged.
//
// It is a byte-level state machine kept across calls, so an escape cut by a PTY read is still
// recognised, and it buffers no payload: at most an ESC or `ESC _` waits for the next byte. The
// zero value is ready to use. It is not safe for concurrent use.
type GraphicsFilter struct {
	state   graphicsState
	control []byte
	// chunked says the previous escape had `m=1`: this one continues its image, whose action
	// and U flag the first chunk gave.
	chunked      bool
	chunkAction  byte
	chunkUnicode bool
}

// Filter appends src, filtered, to dst and returns it. With a nil dst, src holding no escape
// and no escape open, it returns src itself rather than a copy: almost all output is such.
func (f *GraphicsFilter) Filter(dst, src []byte) []byte {
	if dst == nil && f.state == gfxGround && bytes.IndexByte(src, 0x1b) < 0 {
		return src
	}
	for i := 0; i < len(src); i++ {
		b := src[i]
		switch f.state {
		case gfxGround:
			// Copy up to the next ESC in one go: almost all output is ground.
			j := bytes.IndexByte(src[i:], 0x1b)
			if j < 0 {
				return append(dst, src[i:]...)
			}
			dst = append(dst, src[i:i+j]...)
			i += j
			f.state = gfxEsc
		case gfxEsc:
			switch b {
			case '_':
				f.state = gfxApc
			case 0x1b:
				dst = append(dst, 0x1b)
			default:
				dst = append(dst, 0x1b, b)
				f.state = gfxGround
			}
		case gfxApc:
			switch b {
			case 'G':
				f.state, f.control = gfxControl, f.control[:0]
			case 0x1b:
				dst = append(dst, 0x1b, '_')
				f.state = gfxEsc
			default:
				dst = append(dst, 0x1b, '_', b)
				f.state = gfxGround
			}
		case gfxControl:
			switch b {
			case 0x18, 0x1a:
				dst = f.cancel(dst, b)
			case ';':
				f.state = gfxPayload
			case 0x1b:
				f.state = gfxEndOfApc
			default:
				if len(f.control) < maxGraphicsControl {
					f.control = append(f.control, b)
				}
			}
		case gfxPayload:
			// Drop up to the ESC that ends it, or the CAN or SUB that cancels it, in one go.
			j := bytes.IndexAny(src[i:], "\x1b\x18\x1a")
			if j < 0 {
				return dst
			}
			i += j
			if src[i] != 0x1b {
				dst = f.cancel(dst, src[i])
				continue
			}
			f.state = gfxEndOfApc
		case gfxEndOfApc:
			dst = f.finish(dst)
			if b == '\\' {
				f.state = gfxGround
				continue
			}
			// An ESC that is not ST ends the escape and starts another, as a terminal reads it.
			f.state = gfxEsc
			i--
		}
	}
	return dst
}

// cancel ends an escape a CAN or SUB interrupted, as a terminal does: the image is discarded,
// so nothing is written in its place, and the byte passes for the client's own parser.
func (f *GraphicsFilter) cancel(dst []byte, b byte) []byte {
	f.state = gfxGround
	return append(dst, b)
}

// finish reads the ended escape's control data and appends a placeholder when it displays an
// image.
func (f *GraphicsFilter) finish(dst []byte) []byte {
	action, unicode, more := byte('t'), false, false
	for kv := range bytes.SplitSeq(f.control, []byte{','}) {
		k, v, ok := bytes.Cut(kv, []byte{'='})
		if !ok || len(k) != 1 || len(v) == 0 {
			continue
		}
		switch k[0] {
		case 'a':
			action = v[0]
		case 'U':
			unicode = v[0] == '1'
		case 'm':
			more = v[0] == '1'
		}
	}
	if f.chunked {
		action, unicode = f.chunkAction, f.chunkUnicode
	}
	f.chunked = more
	if more {
		f.chunkAction, f.chunkUnicode = action, unicode
		return dst
	}
	if (action == 'T' || action == 'p') && !unicode {
		dst = append(dst, GraphicsPlaceholder...)
	}
	return dst
}
