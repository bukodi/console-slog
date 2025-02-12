package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
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
	//	%t	timestamp
	//	%l	abbreviated level (e.g. "INF")
	//	%L	level (e.g. "INFO")
	//	%m	message
	//	%[key]h	header with the given key.
	//
	// Headers print the value of the attribute with the given key, and remove that
	// attribute from the end of the log line.
	//
	// Headers can be customized with width, alignment, and non-capturing,
	// similar to fmt.Printf verbs. For example:
	//
	//	%[key]10h		// left-aligned, width 10
	//	%[key]-10h		// right-aligned, width 10
	//	%[key]+h		// non-capturing
	//	%[key]-10+h		// right-aligned, width 10, non-capturing
	//
	// If the header is non-capturing, the header field will be printed, but
	// the attribute will still be available for matching subsequent header fields,
	// and/or printing in the attributes section of the log line.
	HeaderFormat string
}

const defaultHeaderFormat = "%t %l %[source]h > %m"

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
	key        string
	width      int
	rightAlign bool
	capture    bool
	memo       string
}
type levelField struct {
	abbreviated bool
	rightAlign  bool
}
type messageField struct{}

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

func (e *encoder) encodeAttr(groupPrefix string, a slog.Attr) {
	offset := e.attrBuf.Len()
	e.writeAttr(&e.attrBuf, a, groupPrefix)

	// check if the last attr written has newlines in it
	// if so, move it to the trailerBuf
	lastAttr := e.attrBuf[offset:]
	if bytes.IndexByte(lastAttr, '\n') >= 0 {
		// todo: consider splitting the key and the value
		// components, so the `key=` can be printed on its
		// own line, and the value will not share any of its
		// lines with anything else.  Like:
		//
		// INF msg key1=val1
		// key2=
		// val2 line 1
		// val2 line 2
		// key3=
		// val3 line 1
		// val3 line 2
		//
		// and maybe consider printing the key for these values
		// differently, like:
		//
		// === key2 ===
		// val2 line1
		// val2 line2
		// === key3 ===
		// val3 line 1
		// val3 line 2
		//
		// Splitting the key and value doesn't work up here in
		// Handle() though, because we don't know where the term
		// control characters are.  Would need to push this
		// multiline handling deeper into encoder, or pass
		// offsets back up from writeAttr()
		//
		// if k, v, ok := bytes.Cut(lastAttr, []byte("=")); ok {
		// trailerBuf.AppendString("=== ")
		// trailerBuf.Append(k[1:])
		// trailerBuf.AppendString(" ===\n")
		// trailerBuf.AppendByte('=')
		// trailerBuf.AppendByte('\n')
		// trailerBuf.AppendString("---------------------\n")
		// trailerBuf.Append(v)
		// trailerBuf.AppendString("\n---------------------\n")
		// trailerBuf.AppendByte('\n')
		// } else {
		// trailerBuf.Append(lastAttr[1:])
		// trailerBuf.AppendByte('\n')
		// }
		e.multilineAttrBuf.Append(lastAttr)

		// rewind the middle buffer
		e.attrBuf = e.attrBuf[:offset]
	}
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	enc := newEncoder(h)

	if h.opts.AddSource && rec.PC > 0 {
		src := slog.Source{}
		frame, _ := runtime.CallersFrames([]uintptr{rec.PC}).Next()
		src.Function = frame.Function
		src.File = frame.File
		src.Line = frame.Line
		rec.AddAttrs(slog.Any(slog.SourceKey, &src))
	}

	headerAttrs := slices.Grow(enc.headerAttrs, len(h.headerFields))[:len(h.headerFields)]
	clear(headerAttrs)

	enc.attrBuf.Append(h.context)
	enc.multilineAttrBuf.Append(h.multilineContext)

	rec.Attrs(func(a slog.Attr) bool {
		for i, f := range h.headerFields {
			if f.key == a.Key {
				headerAttrs[i] = a
				if f.capture {
					return true
				}
			}
		}

		enc.encodeAttr(h.groupPrefix, a)
		return true
	})

	var swallow bool
	headerIdx := 0
	var l int
	for _, f := range h.fields {
		switch f := f.(type) {
		case headerField:
			if headerAttrs[headerIdx].Equal(slog.Attr{}) && f.memo != "" {
				enc.buf.AppendString(f.memo)
			} else {
				enc.writeHeader(&enc.buf, headerAttrs[headerIdx], f.width, f.rightAlign)
			}
			headerIdx++
		case levelField:
			enc.writeLevel(&enc.buf, rec.Level, f.abbreviated)
		case messageField:
			enc.writeMessage(&enc.buf, rec.Level, rec.Message)
		case timestampField:
			enc.writeTimestamp(&enc.buf, rec.Time)
		case string:
			// todo: need to color these strings
			// todo: can we generalize this to some form of grouping?
			if swallow {
				if len(f) > 0 && f[0] == ' ' {
					f = f[1:]
				}
			}
			enc.buf.AppendString(f)
			l = 0 // ensure the next field is not swallowed
		}
		l2 := enc.buf.Len()
		swallow = l2 == l
		l = l2
	}

	// concatenate the buffers together before writing to out, so the entire
	// log line is written in a single Write call
	enc.buf.copy(&enc.attrBuf)
	enc.buf.copy(&enc.multilineAttrBuf)
	enc.NewLine(&enc.buf)

	if _, err := enc.buf.WriteTo(h.out); err != nil {
		return err
	}

	enc.free()
	return nil
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// todo: reuse the encode for memoization
	attrs, fields := h.memoizeHeaders(attrs)

	enc := newEncoder(h)
	for _, a := range attrs {
		enc.encodeAttr(h.groupPrefix, a)
	}

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

	return &Handler{
		opts:             h.opts,
		out:              h.out,
		groupPrefix:      h.groupPrefix,
		context:          newCtx,
		multilineContext: newMultiCtx,
		groups:           h.groups,
		fields:           fields,
		headerFields:     h.headerFields,
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

func (h *Handler) memoizeHeaders(attrs []slog.Attr) ([]slog.Attr, []any) {
	enc := newEncoder(h)
	defer enc.free()
	buf := &enc.buf
	newFields := make([]any, len(h.fields))
	copy(newFields, h.fields)
	remainingAttrs := make([]slog.Attr, 0, len(attrs))

	for _, attr := range attrs {
		capture := false
		for i, field := range h.fields {
			if headerField, ok := field.(headerField); ok {
				if headerField.key == attr.Key {
					buf.Reset()
					enc.writeHeader(buf, attr, headerField.width, headerField.rightAlign)
					headerField.memo = buf.String()
					newFields[i] = headerField
					if headerField.capture {
						capture = true
					}
					// don't break, in case there are multiple headers with the same key
				}
			}
		}
		if !capture {
			remainingAttrs = append(remainingAttrs, attr)
		}
	}
	return remainingAttrs, newFields
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
// %L - non-abbreviated levelField, supports - modifier
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

	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			// Find the next % or end of string
			start := i
			for i < len(format) && format[i] != '%' {
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
			if format[i] == '-' {
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
			field = hf
			headerFields = append(headerFields, hf)
		case 'm':
			field = messageField{}
		case 'l':
			field = levelField{abbreviated: true}
		case 'L':
			field = levelField{
				abbreviated: false,
				rightAlign:  rightAlign,
			}
		default:
			fields = append(fields, fmt.Sprintf("%%!%c(INVALID_VERB)", format[i]))
			continue
		}

		fields = append(fields, field)
	}

	return fields, headerFields
}
