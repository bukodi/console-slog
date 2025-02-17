package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"
)

var cwd string

func init() {
	cwd, _ = os.Getwd()
	// We compare cwd to the filepath in runtime.Frame.File
	// It turns out, an old legacy behavior of go is that runtime.Frame.File
	// will always contain file paths with forward slashes, even if compiled
	// on Windows.
	// See https://github.com/golang/go/issues/3335
	// and https://github.com/golang/go/issues/18151
	cwd = strings.ReplaceAll(cwd, "\\", "/")
}

// HandlerOptions are options for a ConsoleHandler.
// A zero HandlerOptions consists entirely of default values.
// ReplaceAttr works identically to [slog.HandlerOptions.ReplaceAttr]
type HandlerOptions struct {
	// AddSource causes the handler to compute the source code position
	// of the log statement and add a SourceKey attribute to the output.
	AddSource bool

	// Level reports the minimum record level that will be logged.
	// The handler discards records with lower levels.
	// If Level is nil, the handler assumes LevelInfo.
	// The handler calls Level.Level for each record processed;
	// to adjust the minimum level dynamically, use a LevelVar.
	Level slog.Leveler

	// Disable colorized output
	NoColor bool

	// TimeFormat is the format used for time.DateTime
	TimeFormat string

	// Theme defines the colorized output using ANSI escape sequences
	Theme Theme

	// ReplaceAttr is called to rewrite each non-group attribute before it is logged.
	// See [slog.HandlerOptions]
	ReplaceAttr func(groups []string, a slog.Attr) slog.Attr

	// TruncateSourcePath shortens the source file path, if AddSource=true.
	// If 0, no truncation is done.
	// If >0, the file path is truncated to that many trailing path segments.
	// For example:
	//
	//     users.go:34						// TruncateSourcePath = 1
	//     models/users.go:34				// TruncateSourcePath = 2
	//     ...etc
	TruncateSourcePath int

	// HeaderFormat specifies the format of the log header.
	//
	// The default format is "%t %l %[source]h > %m".
	//
	// The format is a string containing verbs, which are expanded as follows:
	//
	//	%t	     timestamp
	//	%l	     abbreviated level (e.g. "INF")
	//	%L	     level (e.g. "INFO")
	//	%m	     message
	//	%[key]h	 header with the given key.
	//  %{       group open
	//  %}       group close
	//
	// Headers print the value of the attribute with the given key, and remove that
	// attribute from the end of the log line.
	//
	// Headers can be customized with width, alignment, and non-capturing modifiers,
	// similar to fmt.Printf verbs. For example:
	//
	//	%[key]10h		// left-aligned, width 10
	//	%[key]-10h		// right-aligned, width 10
	//	%[key]+h		// non-capturing
	//	%[key]-10+h		// right-aligned, width 10, non-capturing
	//
	// Note that headers will "capture" their matching attribute by default, which means that attribute will not
	// be included in the attributes section of the log line, and will not be matched by subsequent header fields.
	// Use the non-capturing header modifier '+' to disable capturing.  If a header is non-capturing, the attribute
	// will still be available for matching subsequent header fields, and will be included in the attributes section
	// of the log line.
	//
	// Groups will omit their contents if all the fields in that group are omitted.  For example:
	//
	//	"%l %{%[logger]h %[source]h > %} %m"
	//
	// will print "INF main main.go:123 > msg" if the either the logger or source attribute is present.  But if the
	// both attributes are not present, or were elided by ReplaceAttr, then this will print "INF msg".  Groups can
	// be nested.
	//
	// Whitespace is generally merged to leave a single space between fields.  Leading and trailing whitespace is trimmed.
	//
	// Examples:
	//
	//	"%t %l %m"                         // timestamp, level, message
	//	"%t [%l] %m"                       // timestamp, level in brackets, message
	//	"%t %l:%m"                         // timestamp, level:message
	//	"%t %l %[key]h %m"                 // timestamp, level, header with key "key", message
	//	"%t %l %[key1]h %[key2]h %m"       // timestamp, level, header with key "key1", header with key "key2", message
	//	"%t %l %[key]10h %m"               // timestamp, level, header with key "key" and width 10, message
	//	"%t %l %[key]-10h %m"              // timestamp, level, right-aligned header with key "key" and width 10, message
	//	"%t %l %[key]10+h %m"              // timestamp, level, captured header with key "key" and width 10, message
	//	"%t %l %[key]-10+h %m"             // timestamp, level, right-aligned captured header with key "key" and width 10, message
	//	"%t %l %L %m"                      // timestamp, abbreviated level, non-abbreviated level, message
	//	"%t %l %L- %m"                     // timestamp, abbreviated level, right-aligned non-abbreviated level, message
	//	"%t %l %m string literal"          // timestamp, level, message, and then " string literal"
	//	"prefix %t %l %m suffix"           // "prefix ", timestamp, level, message, and then " suffix"
	//	"%% %t %l %m"                      // literal "%", timestamp, level, message
	//  "%{[%t]%} %{[%l]%} %m"             // timestamp and level in brackets, message, brackets will be omitted if empty
	HeaderFormat string
}

const defaultHeaderFormat = "%t %l %{%[source]h >%} %m"

type Handler struct {
	opts                      HandlerOptions
	out                       io.Writer
	groupPrefix               string
	groups                    []string
	context, multilineContext buffer
	fields                    []any
	headerFields              []headerField
}

type timestampField struct{}
type headerField struct {
	groupPrefix string
	key         string
	width       int
	rightAlign  bool
	capture     bool
	memo        string
}

type levelField struct {
	abbreviated bool
}
type messageField struct{}

type groupOpen struct{}
type groupClose struct{}

type spacerField struct {
	hard bool
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler creates a Handler that writes to w,
// using the given options.
// If opts is nil, the default options are used.
func NewHandler(out io.Writer, opts *HandlerOptions) *Handler {
	if opts == nil {
		opts = new(HandlerOptions)
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	if opts.TimeFormat == "" {
		opts.TimeFormat = time.DateTime
	}
	if opts.Theme == nil {
		opts.Theme = NewDefaultTheme()
	}
	if opts.HeaderFormat == "" {
		opts.HeaderFormat = defaultHeaderFormat // default format
	}

	fields, headerFields := parseFormat(opts.HeaderFormat)

	// find spocerFields adjacent to string fields and mark them
	// as hard spaces.  hard spaces should not be skipped, only
	// coalesced
	var wasString bool
	lastSpace := -1
	for i, f := range fields {
		switch f.(type) {
		case headerField, levelField, messageField, timestampField:
			wasString = false
			lastSpace = -1
		case string:
			if lastSpace != -1 {
				// string immediately followed space, so the
				// space is hard.
				fields[lastSpace] = spacerField{hard: true}
			}
			wasString = true
			lastSpace = -1
		case spacerField:
			if wasString {
				// space immedately followed a string, so the space
				// is hard
				fields[i] = spacerField{hard: true}
			}
			lastSpace = i
			wasString = false
		}
	}

	return &Handler{
		opts:         *opts, // Copy struct
		out:          out,
		groupPrefix:  "",
		context:      nil,
		fields:       fields,
		headerFields: headerFields,
	}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.opts.Level.Level()
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	enc := newEncoder(h)

	if h.opts.AddSource && rec.PC > 0 {
		src := slog.Source{}
		frame, _ := runtime.CallersFrames([]uintptr{rec.PC}).Next()
		src.Function = frame.Function
		src.File = frame.File
		src.Line = frame.Line
		// the source attr should not be inside any open groups
		groups := enc.groups
		enc.groups = nil
		enc.encodeAttr("", slog.Any(slog.SourceKey, &src))
		enc.groups = groups
		// rec.AddAttrs(slog.Any(slog.SourceKey, &src))
	}

	enc.attrBuf.Append(h.context)
	enc.multilineAttrBuf.Append(h.multilineContext)

	rec.Attrs(func(a slog.Attr) bool {
		enc.encodeAttr(h.groupPrefix, a)
		return true
	})

	headerIdx := 0
	var state encodeState
	// use a fixed size stack to avoid allocations, 3 deep nested groups should be enough for most cases
	stackArr := [3]encodeState{}
	stack := stackArr[:0]
	for _, f := range h.fields {
		switch f := f.(type) {
		case groupOpen:
			stack = append(stack, state)
			state.groupStart = enc.buf.Len()
			state.printedField = false
			continue
		case groupClose:
			if len(stack) == 0 {
				// missing group open
				// no-op
				continue
			}

			if state.printedField {
				// keep the current state, and just roll back
				// the group start index to the prior group
				state.groupStart = stack[len(stack)-1].groupStart
			} else {
				// no fields were printed in this group, so
				// rollback the entire group and pop back to
				// the outer state
				enc.buf.Truncate(state.groupStart)
				state = stack[len(stack)-1]
			}
			// pop a state off the stack
			stack = stack[:len(stack)-1]
			continue
		case spacerField:
			if len(enc.buf) == 0 {
				// special case, always skip leading space
				continue
			}

			if f.hard {
				state.pendingHardSpace = true
			} else {
				// only queue a soft space if the last
				// thing printed was not a string field.
				state.pendingSpace = state.anchored
			}

			continue
		case string:
			if state.pendingHardSpace {
				enc.buf.AppendByte(' ')
			}
			state.pendingHardSpace = false
			state.pendingSpace = false
			state.anchored = false
			enc.withColor(&enc.buf, h.opts.Theme.Header(), func() {
				enc.buf.AppendString(f)
			})
			continue
		}
		if state.pendingSpace || state.pendingHardSpace {
			enc.buf.AppendByte(' ')
		}
		l := enc.buf.Len()
		switch f := f.(type) {
		case headerField:
			hf := h.headerFields[headerIdx]
			if enc.headerAttrs[headerIdx].Equal(slog.Attr{}) && hf.memo != "" {
				enc.buf.AppendString(hf.memo)
			} else {
				enc.encodeHeader(enc.headerAttrs[headerIdx], hf.width, hf.rightAlign)
			}
			headerIdx++

		case levelField:
			enc.encodeLevel(rec.Level, f.abbreviated)
		case messageField:
			enc.encodeMessage(rec.Level, rec.Message)
		case timestampField:
			enc.encodeTimestamp(rec.Time)
		}
		printed := enc.buf.Len() > l
		state.printedField = state.printedField || printed
		if printed {
			state.pendingSpace = false
			state.pendingHardSpace = false
			state.anchored = true
		} else if state.pendingSpace || state.pendingHardSpace {
			// chop the last space
			enc.buf = bytes.TrimSpace(enc.buf)
			// leave state.spacePending as is for next
			// field to handle
		}
	}

	// concatenate the buffers together before writing to out, so the entire
	// log line is written in a single Write call
	enc.buf.copy(&enc.attrBuf)
	enc.buf.copy(&enc.multilineAttrBuf)
	enc.buf.AppendByte('\n')

	if _, err := enc.buf.WriteTo(h.out); err != nil {
		return err
	}

	enc.free()
	return nil
}

type encodeState struct {
	// index in buffer of where the currently open group started.
	// if group ends up being elided, buffer will rollback to this
	// index
	groupStart int
	// whether any field in this group has not been elided.  When a group
	// closes, if this is false, the entire group will be elided
	printedField bool

	anchored, pendingSpace, pendingHardSpace bool
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	enc := newEncoder(h)

	for _, a := range attrs {
		enc.encodeAttr(h.groupPrefix, a)
	}

	headerFields := memoizeHeaders(enc, h.headerFields)

	newCtx := h.context
	newMultiCtx := h.multilineContext
	if len(enc.attrBuf) > 0 {
		newCtx = append(newCtx, enc.attrBuf...)
		newCtx.Clip()
	}
	if len(enc.multilineAttrBuf) > 0 {
		newMultiCtx = append(newMultiCtx, enc.multilineAttrBuf...)
		newMultiCtx.Clip()
	}

	enc.free()

	return &Handler{
		opts:             h.opts,
		out:              h.out,
		groupPrefix:      h.groupPrefix,
		context:          newCtx,
		multilineContext: newMultiCtx,
		groups:           h.groups,
		fields:           h.fields,
		headerFields:     headerFields,
	}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	name = strings.TrimSpace(name)
	groupPrefix := name
	if h.groupPrefix != "" {
		groupPrefix = h.groupPrefix + "." + name
	}
	return &Handler{
		opts:         h.opts,
		out:          h.out,
		groupPrefix:  groupPrefix,
		context:      h.context,
		groups:       append(h.groups, name),
		fields:       h.fields,
		headerFields: h.headerFields,
	}
}

func memoizeHeaders(enc *encoder, headerFields []headerField) []headerField {
	newFields := make([]headerField, len(headerFields))
	copy(newFields, headerFields)

	for i := range newFields {
		if !enc.headerAttrs[i].Equal(slog.Attr{}) {
			enc.buf.Reset()
			enc.encodeHeader(enc.headerAttrs[i], newFields[i].width, newFields[i].rightAlign)
			newFields[i].memo = enc.buf.String()
		}
	}
	return newFields
}

// ParseFormatResult contains the parsed fields and header count from a format string
type ParseFormatResult struct {
	Fields      []any
	HeaderCount int
}

// Equal compares two ParseFormatResults for equality
func (p ParseFormatResult) Equal(other ParseFormatResult) bool {
	if p.HeaderCount != other.HeaderCount {
		return false
	}
	if len(p.Fields) != len(other.Fields) {
		return false
	}
	for i := range p.Fields {
		if fmt.Sprintf("%#v", p.Fields[i]) != fmt.Sprintf("%#v", other.Fields[i]) {
			return false
		}
	}
	return true
}

// parseFormat parses a format string into a list of fields and the number of headerFields.
// Supported format verbs:
// %t - timestampField
// %h - headerField, requires [name] modifier, supports width, - and + modifiers
// %m - messageField
// %l - abbreviated levelField
// %L - non-abbreviated levelField
//
// Modifiers:
// [name]: the key of the attribute to capture as a header, required
// width: int fixed width, optional
// -: for right alignment, optional
// +: for non-capturing header, optional
//
// Examples:
//
//	"%t %l %m"                         // timestamp, level, message
//	"%t [%l] %m"                       // timestamp, level in brackets, message
//	"%t %l:%m"                         // timestamp, level:message
//	"%t %l %[key]h %m"                 // timestamp, level, header with key "key", message
//	"%t %l %[key1]h %[key2]h %m"       // timestamp, level, header with key "key1", header with key "key2", message
//	"%t %l %[key]10h %m"               // timestamp, level, header with key "key" and width 10, message
//	"%t %l %[key]-10h %m"              // timestamp, level, right-aligned header with key "key" and width 10, message
//	"%t %l %[key]10+h %m"              // timestamp, level, captured header with key "key" and width 10, message
//	"%t %l %[key]-10+h %m"             // timestamp, level, right-aligned captured header with key "key" and width 10, message
//	"%t %l %L %m"                      // timestamp, abbreviated level, non-abbreviated level, message
//	"%t %l %L- %m"                     // timestamp, abbreviated level, right-aligned non-abbreviated level, message
//	"%t %l %m string literal"          // timestamp, level, message, and then " string literal"
//	"prefix %t %l %m suffix"           // "prefix ", timestamp, level, message, and then " suffix"
//	"%% %t %l %m"                      // literal "%", timestamp, level, message
//
// Note that headers will "capture" their matching attribute by default, which means that attribute will not
// be included in the attributes section of the log line, and will not be matched by subsequent header fields.
// Use the non-capturing header modifier '+' to disable capturing.  If a header is not capturing, the attribute
// will still be available for matching subsequent header fields, and will be included in the attributes section
// of the log line.
func parseFormat(format string) (fields []any, headerFields []headerField) {
	fields = make([]any, 0)
	headerFields = make([]headerField, 0)

	format = strings.TrimSpace(format)
	lastWasSpace := false

	for i := 0; i < len(format); i++ {
		if format[i] == ' ' {
			if !lastWasSpace {
				fields = append(fields, spacerField{})
				lastWasSpace = true
			}
			continue
		}
		lastWasSpace = false

		if format[i] != '%' {
			// Find the next % or space or end of string
			start := i
			for i < len(format) && format[i] != '%' && format[i] != ' ' {
				i++
			}
			fields = append(fields, format[start:i])
			i-- // compensate for loop increment
			continue
		}

		// Handle %% escape
		if i+1 < len(format) && format[i+1] == '%' {
			fields = append(fields, "%")
			i++
			continue
		}

		// Parse format verb and any modifiers
		i++
		if i >= len(format) {
			fields = append(fields, "%!(MISSING_VERB)")
			break
		}

		// Check for modifiers before verb
		var field any
		var width int
		var rightAlign bool
		var capture bool = true // default to capturing for headers
		var key string

		// Look for [name] modifier
		if format[i] == '[' {
			// Find the next ] or end of string
			end := i + 1
			for end < len(format) && format[end] != ']' && format[end] != ' ' {
				end++
			}
			if end >= len(format) || format[end] != ']' {
				i = end - 1 // Position just before the next character to process
				fields = append(fields, "%!(MISSING_CLOSING_BRACKET)")
				continue
			}
			key = format[i+1 : end]
			i = end + 1
		}

		// Look for modifiers
		for i < len(format) {
			if format[i] == '-' && key != "" { // '-' only valid for headers
				rightAlign = true
				i++
			} else if format[i] == '+' && key != "" { // '+' only valid for headers
				capture = false
				i++
			} else if format[i] >= '0' && format[i] <= '9' && key != "" { // width only valid for headers
				width = 0
				for i < len(format) && format[i] >= '0' && format[i] <= '9' {
					width = width*10 + int(format[i]-'0')
					i++
				}
			} else {
				break
			}
		}

		if i >= len(format) {
			fields = append(fields, "%!(MISSING_VERB)")
			break
		}

		// Parse the verb
		switch format[i] {
		case 't':
			field = timestampField{}
		case 'h':
			if key == "" {
				fields = append(fields, "%!h(MISSING_HEADER_NAME)")
				continue
			}
			hf := headerField{
				key:        key,
				width:      width,
				rightAlign: rightAlign,
				capture:    capture,
			}
			if idx := strings.LastIndexByte(key, '.'); idx > -1 {
				hf.groupPrefix = key[:idx]
				hf.key = key[idx+1:]
			}
			field = hf
			headerFields = append(headerFields, hf)
		case 'm':
			field = messageField{}
		case 'l':
			field = levelField{abbreviated: true}
		case 'L':
			field = levelField{
				abbreviated: false,
			}
		case '{':
			field = groupOpen{}
		case '}':
			field = groupClose{}
		default:
			fields = append(fields, fmt.Sprintf("%%!%c(INVALID_VERB)", format[i]))
			continue
		}

		fields = append(fields, field)
	}

	return fields, headerFields
}
