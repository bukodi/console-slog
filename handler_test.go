package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewHandler(t *testing.T) {
	h := NewHandler(nil, nil)
	AssertEqual(t, time.DateTime, h.opts.TimeFormat)
	AssertEqual(t, NewDefaultTheme().Name(), h.opts.Theme.Name())
	AssertEqual(t, defaultHeaderFormat, h.opts.HeaderFormat)
}

func TestHandler_Enabled(t *testing.T) {
	tests := []slog.Level{
		slog.LevelDebug - 1, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, slog.LevelError + 1,
	}

	for _, lvl := range tests {
		t.Run(lvl.String(), func(t *testing.T) {
			h := NewHandler(io.Discard, &HandlerOptions{Level: lvl})
			if h.Enabled(context.Background(), lvl-1) {
				t.Errorf("Expected %v to be disabled, got: enabled", lvl-1)
			}
			if !h.Enabled(context.Background(), lvl) {
				t.Errorf("Expected %v to be enabled, got: disabled", lvl)
			}
			if !h.Enabled(context.Background(), lvl+1) {
				t.Errorf("Expected %v to be enabled, got: disabled", lvl+1)
			}
		})
	}
}

func TestHandler_TimeFormat(t *testing.T) {
	testTime := time.Date(2024, 01, 02, 15, 04, 05, 123456789, time.UTC)
	tests := []struct {
		name       string
		timeFormat string
		attrs      []slog.Attr
		want       string
	}{
		{
			name:       "DateTime",
			timeFormat: time.DateTime,
			want:       "2024-01-02 15:04:05\n",
		},
		{
			name:       "RFC3339Nano",
			timeFormat: time.RFC3339Nano,
			want:       "2024-01-02T15:04:05.123456789Z\n",
		},
		{
			name:       "Kitchen",
			timeFormat: time.Kitchen,
			want:       "3:04PM\n",
		},
		{
			name:       "EmptyFormat",
			timeFormat: "", // should default to DateTime
			want:       "2024-01-02 15:04:05\n",
		},
		{
			name:       "CustomFormat",
			timeFormat: "2006/01/02 15:04:05.000 MST",
			want:       "2024/01/02 15:04:05.123 UTC\n",
		},
		{
			name:       "also formats attrs",
			timeFormat: time.Kitchen,
			attrs:      []slog.Attr{slog.Time("foo", time.Date(2025, 01, 02, 5, 03, 05, 22, time.UTC))},
			want:       "3:04PM foo=5:03AM\n",
		},
	}

	for _, tt := range tests {
		ht := &handlerTest{
			name: tt.name,
			time: testTime,
			opts: HandlerOptions{
				TimeFormat:   tt.timeFormat,
				NoColor:      true,
				HeaderFormat: "%t %m %a",
			},
			attrs: tt.attrs,
			want:  tt.want,
		}
		ht.runSubtest(t)
	}
}

// Handlers should not log the time field if it is zero.
// '- If r.Time is the zero time, ignore the time.'
// https://pkg.go.dev/log/slog@master#Handler
func TestHandler_TimeZero(t *testing.T) {
	handlerTest{
		opts: HandlerOptions{TimeFormat: time.RFC3339Nano, NoColor: true},
		msg:  "foobar",
		want: "INF foobar\n",
	}.run(t)
}

type theStringer struct{}

func (t theStringer) String() string { return "stringer" }

type noStringer struct {
	Foo string
}

var _ slog.LogValuer = &theValuer{}

type theValuer struct {
	word string
}

// LogValue implements the slog.LogValuer interface.
// This only works if the attribute value is a pointer to theValuer:
//
//	slog.Any("field", &theValuer{"word"}
func (v *theValuer) LogValue() slog.Value {
	return slog.StringValue(fmt.Sprintf("The word is '%s'", v.word))
}

type formatterError struct {
	error
}

func (e *formatterError) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('+') {
		io.WriteString(f, "formatted ")
	}
	io.WriteString(f, e.Error())
}

func TestHandler_Attr(t *testing.T) {
	testTime := time.Date(2024, 01, 02, 15, 04, 05, 123456789, time.UTC)
	handlerTest{
		opts: HandlerOptions{NoColor: true},
		msg:  "foobar",
		time: testTime,
		attrs: []slog.Attr{
			slog.Bool("bool", true),
			slog.Int("int", -12),
			slog.Uint64("uint", 12),
			slog.Float64("float", 3.14),
			slog.String("foo", "bar"),
			slog.Time("time", testTime),
			slog.Duration("dur", time.Second),
			slog.Group("group", slog.String("foo", "bar"), slog.Group("subgroup", slog.String("foo", "bar"))),
			slog.Any("err", errors.New("the error")),
			slog.Any("formattedError", &formatterError{errors.New("the error")}),
			slog.Any("stringer", theStringer{}),
			slog.Any("nostringer", noStringer{Foo: "bar"}),
			// Resolve LogValuer items in addition to Stringer items.
			// '- Attr's values should be resolved.'
			// https://pkg.go.dev/log/slog@master#Handler
			// https://pkg.go.dev/log/slog@master#LogValuer
			slog.Any("valuer", &theValuer{"distant"}),
			// Handlers are supposed to avoid logging empty attributes.
			// '- If an Attr's key and value are both the zero value, ignore the Attr.'
			// https://pkg.go.dev/log/slog@master#Handler
			slog.Attr{},
			slog.Any("", nil),
		},
		want: "2024-01-02 15:04:05 INF foobar bool=true int=-12 uint=12 float=3.14 foo=bar time=2024-01-02 15:04:05 dur=1s group.foo=bar group.subgroup.foo=bar err=the error formattedError=formatted the error stringer=stringer nostringer={bar} valuer=The word is 'distant'\n",
	}.run(t)
}

func TestHandler_AttrsWithNewlines(t *testing.T) {
	tests := []handlerTest{
		{
			name: "single attr",
			attrs: []slog.Attr{
				slog.String("foo", "line one\nline two"),
			},
			want: "INF multiline attrs foo=line one\nline two\n",
		},
		{
			name: "multiple attrs",
			attrs: []slog.Attr{
				slog.String("foo", "line one\nline two"),
				slog.String("bar", "line three\nline four"),
			},
			want: "INF multiline attrs foo=line one\nline two bar=line three\nline four\n",
		},
		{
			name: "sort multiline attrs to end",
			attrs: []slog.Attr{
				slog.String("size", "big"),
				slog.String("foo", "line one\nline two"),
				slog.String("weight", "heavy"),
				slog.String("bar", "line three\nline four"),
				slog.String("color", "red"),
			},
			want: "INF multiline attrs size=big weight=heavy color=red foo=line one\nline two bar=line three\nline four\n",
		},
		{
			name: "multiline message",
			msg:  "multiline\nmessage",
			want: "INF multiline\nmessage\n",
		},
		{
			name: "trim leading and trailing newlines",
			attrs: []slog.Attr{
				slog.String("foo", "\nline one\nline two\n"),
			},
			want: "INF multiline attrs foo=\nline one\nline two\n",
		},
		{
			name: "multiline attr using WithAttrs",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithAttrs([]slog.Attr{
					slog.String("foo", "line one\nline two"),
				})
			},
			attrs: []slog.Attr{slog.String("bar", "baz")},
			want:  "INF multiline attrs bar=baz foo=line one\nline two\n",
		},
		{
			name: "multiline header value",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %[foo]h > %m"},
			attrs: []slog.Attr{
				slog.String("foo", "line one\nline two"),
			},
			want: "INF line one\nline two > multiline attrs\n",
		},
	}

	for _, test := range tests {
		if test.msg == "" {
			test.msg = "multiline attrs"
		}
		test.opts.NoColor = true
		test.runSubtest(t)
	}
}

func TestHandler_Groups(t *testing.T) {
	tests := []handlerTest{
		{
			name: "single group",
			attrs: []slog.Attr{
				slog.Group("group", slog.String("foo", "bar")),
			},
			want: "INF single group group.foo=bar\n",
		},
		{
			// '- If a group has no Attrs (even if it has a non-empty key), ignore it.'
			// https://pkg.go.dev/log/slog@master#Handler
			name: "empty groups should be elided",
			attrs: []slog.Attr{
				slog.Group("group", slog.String("foo", "bar")),
				slog.Group("empty"),
			},
			want: "INF empty groups should be elided group.foo=bar\n",
		},
		{
			// Handlers should expand groups named "" (the empty string) into the enclosing log record.
			// '- If a group's key is empty, inline the group's Attrs.'
			// https://pkg.go.dev/log/slog@master#Handler
			name: "inline group",
			attrs: []slog.Attr{
				slog.Group("group", slog.String("foo", "bar")),
				slog.Group("", slog.String("foo", "bar")),
			},
			want: "INF inline group group.foo=bar foo=bar\n",
		},
		{
			// A Handler should call Resolve on attribute values in groups.
			// https://cs.opensource.google/go/x/exp/+/0dcbfd60:slog/slogtest/slogtest.go
			name: "groups with valuer members",
			attrs: []slog.Attr{
				slog.Group("group", "stringer", theStringer{}, "valuer", &theValuer{"surreal"}),
			},
			want: "INF groups with valuer members group.stringer=stringer group.valuer=The word is 'surreal'\n",
		},
	}

	for _, test := range tests {
		test.opts.NoColor = true
		test.msg = test.name
		test.runSubtest(t)
	}
}

func TestHandler_WithAttr(t *testing.T) {
	testTime := time.Date(2024, 01, 02, 15, 04, 05, 123456789, time.UTC)

	tests := []handlerTest{
		{
			name: "with attrs",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithAttrs([]slog.Attr{
					slog.Bool("bool", true),
					slog.Int("int", -12),
					slog.Uint64("uint", 12),
					slog.Float64("float", 3.14),
					slog.String("foo", "bar"),
					slog.Time("time", testTime),
					slog.Duration("dur", time.Second),
					// A Handler should call Resolve on attribute values from WithAttrs.
					// https://cs.opensource.google/go/x/exp/+/0dcbfd60:slog/slogtest/slogtest.go
					slog.Any("stringer", theStringer{}),
					slog.Any("valuer", &theValuer{"awesome"}),
					slog.Group("group",
						slog.String("foo", "bar"),
						slog.Group("subgroup",
							slog.String("foo", "bar"),
						),
						// A Handler should call Resolve on attribute values in groups from WithAttrs.
						// https://cs.opensource.google/go/x/exp/+/0dcbfd60:slog/slogtest/slogtest.go
						"stringer", theStringer{},
						"valuer", &theValuer{"pizza"},
					),
				})
			},
			msg:  "foobar",
			time: testTime,
			want: "2024-01-02 15:04:05 INF foobar bool=true int=-12 uint=12 float=3.14 foo=bar time=2024-01-02 15:04:05 dur=1s stringer=stringer valuer=The word is 'awesome' group.foo=bar group.subgroup.foo=bar group.stringer=stringer group.valuer=The word is 'pizza'\n",
		},
		{
			name: "multiple withAttrs",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithAttrs([]slog.Attr{
					slog.String("foo", "bar"),
				}).WithAttrs([]slog.Attr{
					slog.String("baz", "buz"),
				})
			},
			want: "INF multiple withAttrs foo=bar baz=buz\n",
		},
		{
			name: "withAttrs and headers",
			opts: HandlerOptions{HeaderFormat: "%l %[foo]h %[bar]h > %m"},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithAttrs([]slog.Attr{
					slog.String("foo", "bar"),
				})
			},
			want: "INF bar > withAttrs and headers\n",
		},
	}

	for _, test := range tests {
		test.opts.NoColor = true
		if test.msg == "" {
			test.msg = test.name
		}
		test.runSubtest(t)
	}

	t.Run("state isolation", func(t *testing.T) {
		// test to make sure the way that WithAttrs() copies the cached headers doesn't leak
		// headers back to the parent handler or to subsequent Handle() calls (i.e. ensure that
		// the headers slice is copied at the right times).

		buf := bytes.Buffer{}
		h := NewHandler(&buf, &HandlerOptions{
			HeaderFormat: "%l %[foo]h %[bar]h > %m",
			TimeFormat:   "0",
			NoColor:      true,
		})

		assertLog := func(t *testing.T, handler slog.Handler, want string, attrs ...slog.Attr) {
			buf.Reset()
			rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "with headers", 0)

			rec.AddAttrs(attrs...)

			AssertNoError(t, handler.Handle(context.Background(), rec))
			AssertEqual(t, want, buf.String())
		}

		assertLog(t, h, "INF bar > with headers\n", slog.String("foo", "bar"))

		h2 := h.WithAttrs([]slog.Attr{slog.String("foo", "baz")})
		assertLog(t, h2, "INF baz > with headers\n")

		h3 := h2.WithAttrs([]slog.Attr{slog.String("foo", "buz")})
		assertLog(t, h3, "INF buz > with headers\n")
		// creating h3 should not have affected h2
		assertLog(t, h2, "INF baz > with headers\n")

		// overriding attrs shouldn't affect the handler
		assertLog(t, h2, "INF biz > with headers\n", slog.String("foo", "biz"))
		assertLog(t, h2, "INF baz > with headers\n")

	})
}

func TestHandler_WithGroup(t *testing.T) {

	tests := []handlerTest{
		{
			name: "withGroup",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1")
			},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF withGroup group1.foo=bar\n",
		},
		{
			name: "withGroup and headers",
			opts: HandlerOptions{HeaderFormat: "%l %[group1.foo]h %[bar]h > %m %a"},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1").WithAttrs([]slog.Attr{slog.String("foo", "bar"), slog.String("bar", "baz")})
			},
			want: "INF bar > withGroup and headers group1.bar=baz\n",
		},
		{
			name: "withGroup and withAttrs",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithAttrs([]slog.Attr{slog.String("bar", "baz")}).WithGroup("group1").WithAttrs([]slog.Attr{slog.String("foo", "bar")})
			},
			attrs: []slog.Attr{slog.String("baz", "foo")},
			want:  "INF withGroup and withAttrs bar=baz group1.foo=bar group1.baz=foo\n",
		},
	}

	for _, test := range tests {
		test.opts.NoColor = true
		if test.msg == "" {
			test.msg = test.name
		}
		test.runSubtest(t)
	}

	t.Run("state isolation", func(t *testing.T) {
		// test to make sure the way that WithGroup() caches state doesn't leak
		// back to the parent handler or to subsequent Handle() calls

		buf := bytes.Buffer{}
		h := NewHandler(&buf, &HandlerOptions{
			HeaderFormat: "%m %a",
			TimeFormat:   "0",
			NoColor:      true,
			// the only state which WithGroup() might corrupt is the list of groups
			// passed to ReplaceAttr.  So we use a custom ReplaceAttr to test that
			// state is not leaked.
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == "foo" {
					return slog.String("foo", strings.Join(groups, "."))
				}
				return a
			},
		})

		assertLog := func(t *testing.T, handler slog.Handler, want string, attrs ...slog.Attr) {
			t.Helper()

			buf.Reset()
			rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "state isolation", 0)

			rec.AddAttrs(attrs...)

			AssertNoError(t, handler.Handle(context.Background(), rec))
			AssertEqual(t, want, buf.String())
		}

		assertLog(t, h, "state isolation foo=\n", slog.String("foo", "bar"))

		h2 := h.WithGroup("group1")
		assertLog(t, h2, "state isolation group1.foo=group1\n", slog.String("foo", "bar"))

		h3 := h.WithGroup("group2")
		assertLog(t, h3, "state isolation group2.foo=group2\n", slog.String("foo", "bar"))
		// creating h3 should not have affected h2
		assertLog(t, h2, "state isolation group1.foo=group1\n", slog.String("foo", "bar"))

		// overriding attrs shouldn't affect the handler
		assertLog(t, h2, "state isolation group1.group3.foo=group1.group3\n", slog.Group("group3", slog.String("foo", "biz")))
		assertLog(t, h3, "state isolation group2.group3.foo=group2.group3\n", slog.Group("group3", slog.String("foo", "biz")))

	})
}

type valuer struct {
	v slog.Value
}

func (v valuer) LogValue() slog.Value {
	return v.v
}

func TestHandler_ReplaceAttr(t *testing.T) {
	pc, file, line, _ := runtime.Caller(0)
	cwd, _ := os.Getwd()
	file, _ = filepath.Rel(cwd, file)
	sourceField := fmt.Sprintf("%s:%d", file, line)

	replaceAttrWith := func(key string, out slog.Attr) func(*testing.T, []string, slog.Attr) slog.Attr {
		return func(t *testing.T, s []string, a slog.Attr) slog.Attr {
			if a.Key == key {
				return out
			}
			return a
		}
	}

	awesomeVal := slog.Any("valuer", valuer{slog.StringValue("awesome")})

	awesomeValuer := valuer{slog.StringValue("awesome")}

	tests := []struct {
		name        string
		replaceAttr func(*testing.T, []string, slog.Attr) slog.Attr
		want        string
		modrec      func(*slog.Record)
		noSource    bool
		groups      []string
	}{
		{
			name: "no replaceattrs",
			want: "2010-05-06 07:08:09 INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name: "not called for empty timestamp and disabled source",
			modrec: func(r *slog.Record) {
				r.Time = time.Time{}
			},
			noSource: true,
			want:     "INF foobar size=12 color=red\n",
			replaceAttr: func(t *testing.T, s []string, a slog.Attr) slog.Attr {
				switch a.Key {
				case slog.TimeKey, slog.SourceKey:
					t.Errorf("replaceAttr should not have been called for %v", a)
				}
				return a
			},
		},
		{
			name:   "not called for groups",
			modrec: func(r *slog.Record) { r.Add(slog.Group("l1", slog.String("flavor", "vanilla"))) },
			replaceAttr: func(t *testing.T, s []string, a slog.Attr) slog.Attr {
				if a.Key == "l1" {
					t.Errorf("should not have been called on group attrs, was called on %v", a)
				}
				return a
			},
			want: "2010-05-06 07:08:09 INF " + sourceField + " > foobar size=12 color=red l1.flavor=vanilla\n",
		},
		{
			name:   "groups arg",
			groups: []string{"l1", "l2"},
			modrec: func(r *slog.Record) {
				r.Add(slog.Group("l3", slog.String("flavor", "vanilla")))
				r.Add(slog.Int("weight", 23))
			},
			replaceAttr: func(t *testing.T, s []string, a slog.Attr) slog.Attr {
				wantGroups := []string{"l1", "l2"}
				switch a.Key {
				case slog.TimeKey, slog.SourceKey, slog.MessageKey, slog.LevelKey:
					if len(s) != 0 {
						t.Errorf("for builtin attr %v, expected no groups, got %v", a.Key, s)
					}
				case "flavor":
					wantGroups = []string{"l1", "l2", "l3"}
					fallthrough
				default:
					if !reflect.DeepEqual(wantGroups, s) {
						t.Errorf("for %v attr, expected %v, got %v", a.Key, wantGroups, s)
					}
				}
				return slog.String(a.Key, a.Key)
			},
			want: "time level source > msg l1.l2.size=size l1.l2.color=color l1.l2.l3.flavor=flavor l1.l2.weight=weight\n",
		},
		{
			name:        "clear timestamp",
			replaceAttr: replaceAttrWith(slog.TimeKey, slog.Time(slog.TimeKey, time.Time{})),
			want:        "INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "clear timestamp attr",
			replaceAttr: replaceAttrWith(slog.TimeKey, slog.Attr{}),
			want:        "INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace timestamp",
			replaceAttr: replaceAttrWith(slog.TimeKey, slog.Time(slog.TimeKey, time.Date(2000, 2, 3, 4, 5, 6, 0, time.UTC))),
			want:        "2000-02-03 04:05:06 INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace timestamp with different kind",
			replaceAttr: replaceAttrWith(slog.TimeKey, slog.String("color", "red")),
			want:        "red INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace timestamp with valuer",
			replaceAttr: replaceAttrWith(slog.TimeKey, awesomeVal),
			want:        "awesome INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace timestamp with time valuer",
			replaceAttr: replaceAttrWith(slog.TimeKey, slog.Any("valuer", valuer{slog.TimeValue(time.Date(2000, 2, 3, 4, 5, 6, 0, time.UTC))})),
			want:        "2000-02-03 04:05:06 INF " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace level",
			replaceAttr: replaceAttrWith(slog.LevelKey, slog.Any(slog.LevelKey, slog.LevelWarn)),
			want:        "2010-05-06 07:08:09 WRN " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "clear level",
			replaceAttr: replaceAttrWith(slog.LevelKey, slog.Any(slog.LevelKey, nil)),
			want:        "2010-05-06 07:08:09 " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace level with different kind",
			replaceAttr: replaceAttrWith(slog.LevelKey, slog.String("color", "red")),
			want:        "2010-05-06 07:08:09 red " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace level with valuer",
			replaceAttr: replaceAttrWith(slog.LevelKey, awesomeVal),
			want:        "2010-05-06 07:08:09 awesome " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "replace level with level valuer",
			replaceAttr: replaceAttrWith(slog.LevelKey, slog.Any("valuer", valuer{slog.AnyValue(slog.LevelWarn)})),
			want:        "2010-05-06 07:08:09 WRN " + sourceField + " > foobar size=12 color=red\n",
		},
		{
			name:        "clear source",
			replaceAttr: replaceAttrWith(slog.SourceKey, slog.Any(slog.SourceKey, nil)),
			want:        "2010-05-06 07:08:09 INF foobar size=12 color=red\n",
		},
		{
			name: "replace source",
			replaceAttr: replaceAttrWith(slog.SourceKey, slog.Any(slog.SourceKey, &slog.Source{
				File: filepath.Join(cwd, "path", "to", "file.go"),
				Line: 33,
			})),
			want: "2010-05-06 07:08:09 INF path/to/file.go:33 > foobar size=12 color=red\n",
		},
		{
			name:        "replace source with different kind",
			replaceAttr: replaceAttrWith(slog.SourceKey, slog.String(slog.SourceKey, "red")),
			want:        "2010-05-06 07:08:09 INF red > foobar size=12 color=red\n",
		},
		{
			name:        "replace source with valuer",
			replaceAttr: replaceAttrWith(slog.SourceKey, slog.Any(slog.SourceKey, awesomeValuer)),
			want:        "2010-05-06 07:08:09 INF awesome > foobar size=12 color=red\n",
		},
		{
			name: "replace source with source valuer",
			replaceAttr: replaceAttrWith(slog.SourceKey, slog.Any(slog.SourceKey, valuer{slog.AnyValue(&slog.Source{
				File: filepath.Join(cwd, "path", "to", "file.go"),
				Line: 33,
			})})),
			want: "2010-05-06 07:08:09 INF path/to/file.go:33 > foobar size=12 color=red\n",
		},
		{
			name:   "empty source", // won't be called because PC is 0
			modrec: func(r *slog.Record) { r.PC = 0 },
			replaceAttr: func(t *testing.T, s []string, a slog.Attr) slog.Attr {
				if a.Key == slog.SourceKey {
					t.Errorf("should not have been called on source attr, was called on %v", a)
				}
				return a
			},
			want: "2010-05-06 07:08:09 INF foobar size=12 color=red\n",
		},
		{
			name:        "clear message",
			replaceAttr: replaceAttrWith(slog.MessageKey, slog.Any(slog.MessageKey, nil)),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > size=12 color=red\n",
		},
		{
			name:        "replace message",
			replaceAttr: replaceAttrWith(slog.MessageKey, slog.String(slog.MessageKey, "barbaz")),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > barbaz size=12 color=red\n",
		},
		{
			name:        "replace message with different kind",
			replaceAttr: replaceAttrWith(slog.MessageKey, slog.Int(slog.MessageKey, 5)),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > 5 size=12 color=red\n",
		},
		{
			name:        "replace message with valuer",
			replaceAttr: replaceAttrWith(slog.MessageKey, awesomeVal),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > awesome size=12 color=red\n",
		},
		{
			name:        "clear attr",
			replaceAttr: replaceAttrWith("size", slog.Attr{}),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > foobar color=red\n",
		},
		{
			name:        "replace attr",
			replaceAttr: replaceAttrWith("size", slog.String("flavor", "vanilla")),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > foobar flavor=vanilla color=red\n",
		},
		{
			name:        "replace with group attrs",
			replaceAttr: replaceAttrWith("size", slog.Group("l1", slog.String("flavor", "vanilla"))),
			want:        "2010-05-06 07:08:09 INF " + sourceField + " > foobar l1.flavor=vanilla color=red\n",
		},
		// {
		// 	name: "replace header",
		// }
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buf := bytes.Buffer{}

			rec := slog.NewRecord(time.Date(2010, 5, 6, 7, 8, 9, 0, time.UTC), slog.LevelInfo, "foobar", pc)
			rec.Add("size", 12, "color", "red")

			if test.modrec != nil {
				test.modrec(&rec)
			}

			var replaceAttr func([]string, slog.Attr) slog.Attr
			if test.replaceAttr != nil {
				replaceAttr = func(s []string, a slog.Attr) slog.Attr {
					return test.replaceAttr(t, s, a)
				}
			}

			var h slog.Handler = NewHandler(&buf, &HandlerOptions{AddSource: !test.noSource, NoColor: true, ReplaceAttr: replaceAttr})

			for _, group := range test.groups {
				h = h.WithGroup(group)
			}

			AssertNoError(t, h.Handle(context.Background(), rec))

			AssertEqual(t, test.want, buf.String())

		})
	}

}

func TestHandler_TruncateSourcePath(t *testing.T) {
	origCwd := cwd
	t.Cleanup(func() { cwd = origCwd })

	cwd = "/usr/share/proj"
	absSource := slog.Source{
		File: "/var/proj/red/blue/green/yellow/main.go",
		Line: 23,
	}
	relSource := slog.Source{
		File: "/usr/share/proj/red/blue/green/yellow/main.go",
		Line: 23,
	}

	tests := []handlerTest{
		{
			name:  "abs 1",
			opts:  HandlerOptions{TruncateSourcePath: 1},
			attrs: []slog.Attr{slog.Any("source", &absSource)},
			want:  "INF source=main.go:23",
		},
		{
			name:  "abs 2",
			opts:  HandlerOptions{TruncateSourcePath: 2},
			attrs: []slog.Attr{slog.Any("source", &absSource)},
			want:  "INF source=yellow/main.go:23",
		},
		{
			name:  "abs 3",
			opts:  HandlerOptions{TruncateSourcePath: 3},
			attrs: []slog.Attr{slog.Any("source", &absSource)},
			want:  "INF source=green/yellow/main.go:23",
		},
		{
			name:  "abs 4",
			opts:  HandlerOptions{TruncateSourcePath: 4},
			attrs: []slog.Attr{slog.Any("source", &absSource)},
			want:  "INF source=blue/green/yellow/main.go:23",
		},
		{
			name:  "default",
			attrs: []slog.Attr{slog.Any("source", &absSource)},
			want:  "INF source=/var/proj/red/blue/green/yellow/main.go:23",
		},
		{
			name:  "relative",
			attrs: []slog.Attr{slog.Any("source", &relSource)},
			want:  "INF source=red/blue/green/yellow/main.go:23",
		},
		{
			name:  "relative 1",
			opts:  HandlerOptions{TruncateSourcePath: 1},
			attrs: []slog.Attr{slog.Any("source", &relSource)},
			want:  "INF source=main.go:23",
		},
		{
			name:  "relative 2",
			opts:  HandlerOptions{TruncateSourcePath: 2},
			attrs: []slog.Attr{slog.Any("source", &relSource)},
			want:  "INF source=yellow/main.go:23",
		},
		{
			name:  "relative 3",
			opts:  HandlerOptions{TruncateSourcePath: 3},
			attrs: []slog.Attr{slog.Any("source", &relSource)},
			want:  "INF source=green/yellow/main.go:23",
		},
		{
			name:  "relative 4",
			opts:  HandlerOptions{TruncateSourcePath: 4},
			attrs: []slog.Attr{slog.Any("source", &relSource)},
			want:  "INF source=blue/green/yellow/main.go:23",
		},
	}

	for _, tt := range tests {
		tt.opts.NoColor = true
		tt.want += "\n"
		tt.runSubtest(t)
	}
}

func TestHandler_CollapseSpaces(t *testing.T) {
	tests2 := []struct {
		desc, format, want string
	}{
		{"default", "", "INF msg"},
		{"trailing space", "%l ", "INF"},
		{"trailing space", "%l %t ", "INF"},
		{"leading space", " %l", "INF"},
		{"leading space", " %t %l", "INF"},
		{"unanchored", "%l%t %t%l", "INF INF"},
		{"unanchored", "%l%t %l", "INF INF"},
		{"unanchored", "%l %t%l", "INF INF"},
		{"unanchored", "%l %t %l", "INF INF"},
		{"unanchored", "%l %t %t %l", "INF INF"},
		{"unanchored", "%l %t", "INF"},
		{"unanchored", "%t %l", "INF"},
		{"unanchored", "%l %t%t %l", "INF INF"},
		{"unanchored", "[%l %t]", "[INF]"},
		{"unanchored", "[%t %l]", "[INF]"},
		{"unanchored", "[%l %t %l]", "[INF INF]"},
		{"unanchored", "[%l%t %l]", "[INF INF]"},
		{"unanchored", "[%l %t%l]", "[INF INF]"},
		{"unanchored", "[%l%t %t%l]", "[INF INF]"},
		{"extra spaces", "  %l    %t  %t %l   ", "INF INF"},
		{"anchored", "%l %t > %m", "INF > msg"},
		{"anchored", "[%l] [%t] > %m", "[INF] [] > msg"},
		{"anchored", "[ %l %t]", "[ INF]"},
		{"anchored", "[%l %t ]", "[INF ]"},
		{"anchored", "[%t]", "[]"},
		{"anchored", "[ %t ]", "[ ]"},
		{"groups", "%l %{%t%} %l", "INF INF"},
		{"groups", "%l %{ %t %} %l", "INF INF"},
		{"groups", "%l %{ %t %l%} %l", "INF INF INF"},
		{"groups", "%l %{ %t %l %} %l", "INF INF INF"},
		{"groups", "%l %{%l %t %l %} %l", "INF INF INF INF"},
		{"groups", "%l %{ %l %t %l %} %l", "INF INF INF INF"},
		{"groups", "%l %{ %t %t %t %} %l", "INF INF"},
		{"groups", "%l%{%t %} > %m", "INF > msg"},
		{"groups", "%l%{ %t %}%l", "INFINF"},
		{"groups with strings", "%l %{> %t %} %l", "INF INF"},
		{"groups with strings", "%l %{> %t %t %} %l", "INF INF"},
		{"groups with strings", "%l %{%t %t > %} %l", "INF INF"},
		{"groups with strings", "%l %{[%t][%l][%t]%} > ", "INF [][INF][] >"},
		{"groups with strings", "%l %{[%t]%}%{[%l]%}%{[%t]%} > %m", "INF [INF] > msg"},
		{"padded header", "%l %[foo]5h > %m", "INF       > msg"},
		{"nested groups", "%l %{ %{ %{ %t %} %} %} > %m", "INF > msg"},
		{"nested groups", "%l%{ %{ %{%t%}%}%} > %m", "INF > msg"},
		{"deeply nested groups", "%l%{ %{ %{ %{ %{ %{ %t %} %} %} %} %} %} > %m", "INF > msg"},
	}

	for _, tt := range tests2 {
		handlerTest{
			name: tt.desc,
			msg:  "msg",
			opts: HandlerOptions{HeaderFormat: tt.format, NoColor: true},
			want: tt.want + "\n",
		}.runSubtest(t)
	}
}

func styled(s string, c ANSIMod) string {
	if c == "" {
		return s
	}
	return strings.Join([]string{string(c), s, string(ResetMod)}, "")
}

func TestHandler_HeaderFormat_Groups(t *testing.T) {
	theme := NewDefaultTheme()
	tests := []handlerTest{
		{
			name:  "group not elided",
			opts:  HandlerOptions{HeaderFormat: "%l %{[%[foo]h]%} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF [bar] > groups\n",
		},
		{
			name: "group elided",
			opts: HandlerOptions{HeaderFormat: "%l %{[%[foo]h]%} > %m", NoColor: true},
			want: "INF > groups\n",
		},
		{
			name: "group with only fixed strings not elided",
			opts: HandlerOptions{HeaderFormat: "%l %{[fixed string]%} > %m", NoColor: true},
			want: "INF [fixed string] > groups\n",
		},
		{
			name: "two headers in group, both elided",
			opts: HandlerOptions{HeaderFormat: "%l %{[%[foo]h %[bar]h]%} > %m", NoColor: true},
			want: "INF > groups\n",
		},
		{
			name:  "two headers in group, one elided",
			opts:  HandlerOptions{HeaderFormat: "%l %{[%[foo]h %[bar]h]%} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF [bar] > groups\n",
		},
		{
			name:  "two headers in group, neither elided",
			opts:  HandlerOptions{HeaderFormat: "%l %{[%[foo]h %[bar]h]%} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar"), slog.String("bar", "baz")},
			want:  "INF [bar baz] > groups\n",
		},
		{
			name: "open group not closed",
			opts: HandlerOptions{HeaderFormat: "%l %{ > %m", NoColor: true},
			want: "INF > groups\n",
		},
		{
			name: "closed group not opened",
			opts: HandlerOptions{HeaderFormat: "%l %} > %m", NoColor: true},
			want: "INF > groups\n",
		},
		{
			name:  "styled group",
			opts:  HandlerOptions{HeaderFormat: "%l %(source){ [%[foo]h] %} > %m"},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want: strings.Join([]string{
				styled("INF", theme.LevelInfo()), " ",
				styled("[", theme.Source()),
				styled("bar", theme.Header()),
				styled("]", theme.Source()), " ",
				styled(">", theme.Header()), " ",
				styled("groups", theme.Message()),
				"\n"}, ""),
		},
		{
			name:  "nested styled groups",
			opts:  HandlerOptions{HeaderFormat: "%l %(source){ [%[foo]h] %(message){ [%[bar]h] %} %} > %m"},
			attrs: []slog.Attr{slog.String("foo", "bar"), slog.String("bar", "baz")},
			want: strings.Join([]string{
				styled("INF", theme.LevelInfo()), " ",
				styled("[", theme.Source()),
				styled("bar", theme.Header()),
				styled("]", theme.Source()), " ",
				styled("[", theme.Message()),
				styled("baz", theme.Header()),
				styled("]", theme.Message()), " ",
				styled(">", theme.Header()), " ",
				styled("groups", theme.Message()),
				"\n"}, ""),
		},
		{
			name:  "invalid style name",
			opts:  HandlerOptions{HeaderFormat: "%l %(nonexistent){ %[foo]h %} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF %!{(nonexistent)(INVALID_STYLE_MODIFIER) bar > groups\n",
		},
		{
			name:  "unclosed style modifier",
			opts:  HandlerOptions{HeaderFormat: "%l %(source{ %[foo]h %} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF %!(source{(MISSING_CLOSING_PARENTHESIS) bar > groups\n",
		},
		{
			name:  "empty style modifier",
			opts:  HandlerOptions{HeaderFormat: "%l %(){ %[foo]h %} > %m", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF bar > groups\n",
		},
	}

	for _, tt := range tests {
		tt.msg = "groups"
		tt.runSubtest(t)
	}
}

// Add a test for header formats with groups
// nested
// extra open/close groups

func TestHandler_HeaderFormat(t *testing.T) {
	pc, file, line, _ := runtime.Caller(0)
	cwd, _ := os.Getwd()
	file, _ = filepath.Rel(cwd, file)
	sourceField := fmt.Sprintf("%s:%d", file, line)

	testTime := time.Date(2024, 01, 02, 15, 04, 05, 123456789, time.UTC)

	tests := []handlerTest{
		{
			name:  "default",
			opts:  HandlerOptions{AddSource: true, NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "2024-01-02 15:04:05 INF " + sourceField + " > with headers foo=bar\n",
		},
		{
			name: "one header",
			opts: HandlerOptions{HeaderFormat: "%l %[foo]h > %m %a", NoColor: true},
			attrs: []slog.Attr{
				slog.String("foo", "bar"),
				slog.String("bar", "baz"),
			},
			want: "INF bar > with headers bar=baz\n",
		},
		{
			name: "two headers",
			opts: HandlerOptions{HeaderFormat: "%l %[foo]h %[bar]h > %m %a", NoColor: true},
			attrs: []slog.Attr{
				slog.String("foo", "bar"),
				slog.String("bar", "baz"),
			},
			want: "INF bar baz > with headers\n",
		},
		{
			name: "two headers alt order",
			opts: HandlerOptions{HeaderFormat: "%l %[foo]h %[bar]h > %m %a", NoColor: true},
			attrs: []slog.Attr{
				slog.String("bar", "baz"),
				slog.String("foo", "bar"),
			},
			want: "INF bar baz > with headers\n",
		},
		{
			name:  "missing headers",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]h %[bar]h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF bar > with headers\n", // missing headers are omitted
		},
		{
			name:  "missing headers, no space",
			opts:  HandlerOptions{HeaderFormat: "%l%[foo]h%[bar]h>%m %a", NoColor: true}, // no spaces between headers or level/message
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INFbar>with headers\n",
		},
		{
			name:  "header without group prefix does not match attr in group",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]h > %m %a", NoColor: true}, // header is an attribute inside a group
			attrs: []slog.Attr{slog.String("foo", "bar")},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1")
			},
			want: "INF > with headers group1.foo=bar\n", // header is foo, not group1.foo
		},
		{
			name:  "header with group prefix",
			opts:  HandlerOptions{HeaderFormat: "%l %[group1.foo]h > %m %a", NoColor: true}, // header is an attribute inside a group
			attrs: []slog.Attr{slog.String("foo", "bar")},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1")
			},
			want: "INF bar > with headers\n",
		},
		{
			name:  "header in nested groups",
			opts:  HandlerOptions{HeaderFormat: "%l %[group1.group2.foo]h > %m %a", NoColor: true}, // header is an attribute inside a group
			attrs: []slog.Attr{slog.String("foo", "bar")},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1").WithGroup("group2")
			},
			want: "INF bar > with headers\n",
		},
		{
			name:  "header in group attr, no match",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]h > %m %a", NoColor: true}, // header is an attribute inside a group
			attrs: []slog.Attr{slog.Group("group1", slog.String("foo", "bar"))},
			want:  "INF > with headers group1.foo=bar\n",
		},
		{
			name:  "header in group attr, match",
			opts:  HandlerOptions{HeaderFormat: "%l %[group1.foo]h > %m %a", NoColor: true}, // header is an attribute inside a group
			attrs: []slog.Attr{slog.Group("group1", slog.String("foo", "bar"))},
			want:  "INF bar > with headers\n",
		},
		{
			name:  "header and withGroup and nested group",
			opts:  HandlerOptions{HeaderFormat: "%l %[group1.foo]h %[group1.group2.bar]h > %m %a", NoColor: true}, // header is group2.attr0, attr0 is in root
			attrs: []slog.Attr{slog.String("foo", "bar"), slog.Group("group2", slog.String("bar", "baz"))},
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1")
			},
			want: "INF bar baz > with headers\n",
		},
		{
			name:  "no header",
			opts:  HandlerOptions{HeaderFormat: "%l > %m %a", NoColor: true}, // no header
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF > with headers foo=bar\n",
		},
		{
			name: "just level",
			opts: HandlerOptions{HeaderFormat: "%l", NoColor: true}, // no header, no message
			want: "INF\n",
		},
		{
			name: "just message",
			opts: HandlerOptions{HeaderFormat: "%m", NoColor: true}, // just message
			want: "with headers\n",
		},
		{
			name:  "just attrs",
			opts:  HandlerOptions{HeaderFormat: "%a", NoColor: true}, // just attrs
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "foo=bar\n",
		},
		{
			name: "source not in the header",
			handlerFunc: func(h slog.Handler) slog.Handler {
				return h.WithGroup("group1").WithAttrs([]slog.Attr{slog.String("foo", "bar")})
			},
			opts: HandlerOptions{HeaderFormat: "%l > %m %a", NoColor: true, AddSource: true}, // header is foo, not source
			want: "INF > with headers source=" + sourceField + " group1.foo=bar\n",
		},
		{
			name:  "header matches a group attr should skip header",
			attrs: []slog.Attr{slog.Group("group1", slog.String("foo", "bar"))},
			opts:  HandlerOptions{HeaderFormat: "%l %[group1]h > %m %a", NoColor: true},
			want:  "INF > with headers group1.foo=bar\n",
		},
		{
			name:  "repeated header with capture",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]h %[foo]h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF bar > with headers\n", // Second header is ignored since foo was captured by first header
		},
		{
			name:  "non-capturing header",
			opts:  HandlerOptions{HeaderFormat: "%l %[logger]h %[request_id]+h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("logger", "app"), slog.String("request_id", "123")},
			want:  "INF app 123 > with headers request_id=123\n",
		},
		{
			name:  "non-capturing header captured by another header",
			opts:  HandlerOptions{HeaderFormat: "%l %[logger]+h %[logger]h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("logger", "app")},
			want:  "INF app app > with headers\n",
		},
		{
			name:  "multiple non-capturing headers matching same attr",
			opts:  HandlerOptions{HeaderFormat: "%l %[logger]+h %[logger]+h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("logger", "app")},
			want:  "INF app app > with headers logger=app\n",
		},
		{
			name:  "repeated timestamp, level and message fields",
			opts:  HandlerOptions{HeaderFormat: "%t %l %m %t %l %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "2024-01-02 15:04:05 INF with headers 2024-01-02 15:04:05 INF with headers foo=bar\n",
		},
		{
			name:  "missing header and multiple spaces",
			opts:  HandlerOptions{HeaderFormat: "%l   %[missing]h  %[foo]h  >  %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF bar > with headers\n",
		},
		{
			name:  "fixed width header left aligned",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]10h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF bar        > with headers\n",
		},
		{
			name:  "fixed width header right aligned",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]-10h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF        bar > with headers\n",
		},
		{
			name:  "fixed width header truncated",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]3h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "barbaz")},
			want:  "INF bar > with headers\n",
		},
		{
			name:  "fixed width header with spaces",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]10h %[bar]5h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "hello"), slog.String("bar", "world")},
			want:  "INF hello      world > with headers\n",
		},
		{
			name:  "fixed width non-capturing header",
			opts:  HandlerOptions{HeaderFormat: "%l %[foo]+-10h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF        bar > with headers foo=bar\n",
		},
		{
			name:  "fixed width header missing attr",
			opts:  HandlerOptions{HeaderFormat: "%l %[missing]10h > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF            > with headers foo=bar\n",
		},
		{
			name:  "non-abbreviated levels",
			opts:  HandlerOptions{HeaderFormat: "%L > %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INFO > with headers foo=bar\n",
		},
		{
			name:  "alternate text",
			opts:  HandlerOptions{HeaderFormat: "prefix [%l] [%[foo]h] %m suffix > %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "prefix [INF] [bar] with headers suffix >\n",
		},
		{
			name:  "escaped percent",
			opts:  HandlerOptions{HeaderFormat: "prefix %% [%l] %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "prefix % [INF] with headers foo=bar\n",
		},
		{
			name:  "missing verb",
			opts:  HandlerOptions{HeaderFormat: "%m % %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!(MISSING_VERB) foo=bar\n",
		},
		{
			name:  "missing verb with modifiers",
			opts:  HandlerOptions{HeaderFormat: "%m %[slog]+-4 %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!(MISSING_VERB) foo=bar\n",
		},
		{
			name:  "invalid right align modifier",
			opts:  HandlerOptions{HeaderFormat: "%m %-L %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!-(INVALID_MODIFIER)L foo=bar\n",
		},
		{
			name:  "invalid width modifier",
			opts:  HandlerOptions{HeaderFormat: "%m %43L %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!43(INVALID_MODIFIER)L foo=bar\n",
		},
		{
			name:  "invalid style modifier",
			opts:  HandlerOptions{HeaderFormat: "%m %(source)L %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!((INVALID_MODIFIER)L foo=bar\n",
		},
		{
			name:  "invalid key modifier",
			opts:  HandlerOptions{HeaderFormat: "%m %[source]L %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %![(INVALID_MODIFIER)L foo=bar\n",
		},
		{
			name:  "invalid verb",
			opts:  HandlerOptions{HeaderFormat: "%l %x %m %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "INF %!x(INVALID_VERB) with headers foo=bar\n",
		},
		{
			name:  "missing header name",
			opts:  HandlerOptions{HeaderFormat: "%m %h %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %!h(MISSING_HEADER_NAME) foo=bar\n",
		},
		{
			name:  "missing closing bracket in header",
			opts:  HandlerOptions{HeaderFormat: "%m %[fooh > %a", NoColor: true},
			attrs: []slog.Attr{slog.String("foo", "bar")},
			want:  "with headers %![fooh(MISSING_CLOSING_BRACKET) > foo=bar\n",
		},
		{
			name: "zero PC",
			opts: HandlerOptions{HeaderFormat: "%l %[source]h > %m %a", NoColor: true, AddSource: true},
			recFunc: func(r *slog.Record) {
				r.PC = 0
			},
			want: "INF > with headers\n",
		},
		{
			name: "level DEBUG-3",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %L >"},
			recFunc: func(r *slog.Record) {
				r.Level = slog.LevelDebug - 3
			},
			want: "DBG-3 DEBUG-3 >\n",
		},
		{
			name: "level DEBUG+1",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %L >"},
			recFunc: func(r *slog.Record) {
				r.Level = slog.LevelDebug + 1
			},
			want: "DBG+1 DEBUG+1 >\n",
		},
		{
			name: "level INFO+1",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %L >"},
			recFunc: func(r *slog.Record) {
				r.Level = slog.LevelInfo + 1
			},
			want: "INF+1 INFO+1 >\n",
		},
		{
			name: "level WARN +1",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %L >"},
			recFunc: func(r *slog.Record) {
				r.Level = slog.LevelWarn + 1
			},
			want: "WRN+1 WARN+1 >\n",
		},
		{
			name: "level ERROR+1",
			opts: HandlerOptions{NoColor: true, HeaderFormat: "%l %L >"},
			recFunc: func(r *slog.Record) {
				r.Level = slog.LevelError + 1
			},
			want: "ERR+1 ERROR+1 >\n",
		},
	}

	for _, tt := range tests {
		tt.msg = "with headers"
		tt.pc = pc
		tt.lvl = slog.LevelInfo
		tt.time = testTime
		tt.runSubtest(t)
	}
}

type handlerTest struct {
	name        string
	opts        HandlerOptions
	msg         string
	pc          uintptr
	lvl         slog.Level
	time        time.Time
	attrs       []slog.Attr
	handlerFunc func(h slog.Handler) slog.Handler
	recFunc     func(r *slog.Record)
	want        string
}

func (ht handlerTest) runSubtest(t *testing.T) {
	t.Helper()
	t.Run(ht.name, func(t *testing.T) {
		ht.run(t)
	})
}

func (ht handlerTest) run(t *testing.T) {
	t.Helper()
	buf := bytes.Buffer{}
	var h slog.Handler = NewHandler(&buf, &ht.opts)

	rec := slog.NewRecord(ht.time, ht.lvl, ht.msg, ht.pc)
	rec.AddAttrs(ht.attrs...)

	if ht.handlerFunc != nil {
		h = ht.handlerFunc(h)
	}

	if ht.recFunc != nil {
		ht.recFunc(&rec)
	}

	err := h.Handle(context.Background(), rec)
	t.Log("format:", ht.opts.HeaderFormat)
	t.Log(buf.String())
	AssertNoError(t, err)
	AssertEqual(t, ht.want, buf.String())
}

func TestHandler_writerErr(t *testing.T) {
	w := writerFunc(func(b []byte) (int, error) { return 0, errors.New("nope") })
	h := NewHandler(w, &HandlerOptions{NoColor: true})
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "foobar", 0)
	AssertError(t, h.Handle(context.Background(), rec))
}

func TestThemes(t *testing.T) {
	pc, file, line, _ := runtime.Caller(0)
	cwd, _ := os.Getwd()
	file, _ = filepath.Rel(cwd, file)
	sourceField := fmt.Sprintf("%s:%d", file, line)

	testTime := time.Date(2024, 01, 02, 15, 04, 05, 123456789, time.UTC)

	for _, theme := range []Theme{
		NewDefaultTheme(),
		NewBrightTheme(),
	} {
		t.Run(theme.Name(), func(t *testing.T) {
			tests := []struct {
				lvl        slog.Level
				msg        string
				args       []any
				wantLvlStr string
			}{
				{
					msg:        "Access",
					lvl:        slog.LevelDebug - 1,
					wantLvlStr: "DBG-1",
					args: []any{
						"database", "myapp", "host", "localhost:4962",
					},
				},
				{
					msg:        "Access",
					lvl:        slog.LevelDebug,
					wantLvlStr: "DBG",
					args: []any{
						"database", "myapp", "host", "localhost:4962",
					},
				},
				{
					msg:        "Access",
					lvl:        slog.LevelDebug + 1,
					wantLvlStr: "DBG+1",
					args: []any{
						"database", "myapp", "host", "localhost:4962",
					},
				},
				{
					msg:        "Starting listener",
					lvl:        slog.LevelInfo,
					wantLvlStr: "INF",
					args: []any{
						"listen", ":8080",
					},
				},
				{
					msg:        "Access",
					lvl:        slog.LevelInfo + 1,
					wantLvlStr: "INF+1",
					args: []any{
						"method", "GET", "path", "/users", "resp_time", time.Millisecond * 10,
					},
				},
				{
					msg:        "Slow request",
					lvl:        slog.LevelWarn,
					wantLvlStr: "WRN",
					args: []any{
						"method", "POST", "path", "/posts", "resp_time", time.Second * 532,
					},
				},
				{
					msg:        "Slow request",
					lvl:        slog.LevelWarn + 1,
					wantLvlStr: "WRN+1",
					args: []any{
						"method", "POST", "path", "/posts", "resp_time", time.Second * 532,
					},
				},
				{
					msg:        "Database connection lost",
					lvl:        slog.LevelError,
					wantLvlStr: "ERR",
					args: []any{
						"database", "myapp", "error", errors.New("connection reset by peer"),
					},
				},
				{
					msg:        "Database connection lost",
					lvl:        slog.LevelError + 1,
					wantLvlStr: "ERR+1",
					args: []any{
						"database", "myapp", "error", errors.New("connection reset by peer"),
					},
				},
			}

			for _, tt := range tests {
				// put together the expected log line

				var levelStyle ANSIMod
				switch {
				case tt.lvl >= slog.LevelError:
					levelStyle = theme.LevelError()
				case tt.lvl >= slog.LevelWarn:
					levelStyle = theme.LevelWarn()
				case tt.lvl >= slog.LevelInfo:
					levelStyle = theme.LevelInfo()
				default:
					levelStyle = theme.LevelDebug()
				}

				var messageStyle ANSIMod
				switch {
				case tt.lvl >= slog.LevelInfo:
					messageStyle = theme.Message()
				default:
					messageStyle = theme.MessageDebug()
				}

				withAttrs := []slog.Attr{{Key: "pid", Value: slog.IntValue(37556)}}
				attrs := withAttrs
				var rec slog.Record
				rec.Add(tt.args...)
				rec.Attrs(func(a slog.Attr) bool {
					attrs = append(attrs, a)
					return true
				})

				want := styled(testTime.Format(time.Kitchen), theme.Timestamp()) +
					" " +
					styled(tt.wantLvlStr, levelStyle) +
					" " +
					styled("http", theme.Header()) +
					" " +
					styled(sourceField, theme.Source()) +
					" " +
					styled(">", theme.Header()) +
					" " +
					styled(tt.msg, messageStyle)

				for _, attr := range attrs {
					if attr.Key == "error" {
						want += " " +
							styled(attr.Key+"=", theme.AttrKey()) +
							styled(attr.Value.String(), theme.AttrValueError())
					} else {
						want += " " +
							styled(attr.Key+"=", theme.AttrKey()) +
							styled(attr.Value.String(), theme.AttrValue())
					}
				}
				want += "\n"

				ht := handlerTest{
					opts: HandlerOptions{
						AddSource:    true,
						TimeFormat:   time.Kitchen,
						Theme:        theme,
						HeaderFormat: "%t %l %{%[logger]h %s >%} %m %a",
					},
					attrs: append(withAttrs, slog.String("logger", "http")),
					pc:    pc,
					time:  testTime,
					want:  want,
					lvl:   tt.lvl,
					msg:   tt.msg,
					recFunc: func(r *slog.Record) {
						r.Add(tt.args...)
					},
				}
				t.Run(tt.wantLvlStr, ht.run)
			}
		})
	}
}
