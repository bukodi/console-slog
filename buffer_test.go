package console

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestBuffer_Append(t *testing.T) {
	var b buffer
	AssertZero(t, len(b))
	b.AppendString("foobar")
	AssertEqual(t, 6, len(b))
	b.AppendString("baz")
	AssertEqual(t, 9, len(b))
	AssertEqual(t, "foobarbaz", b.String())

	b.AppendByte('.')
	AssertEqual(t, 10, len(b))
	AssertEqual(t, "foobarbaz.", b.String())

	b.AppendBool(true)
	b.AppendBool(false)
	b.AppendFloat(3.14)
	b.AppendInt(42)
	b.AppendUint(12)
	b.Append([]byte("foo"))
	b.AppendDuration(1 * time.Second)
	now := time.Now()
	b.AppendTime(now, time.RFC3339)

	AssertEqual(t, "foobarbaz.truefalse3.144212foo1s"+now.Format(time.RFC3339), b.String())
}

func TestBuffer_WriteTo(t *testing.T) {
	dest := bytes.Buffer{}
	var b buffer
	n, err := b.WriteTo(&dest)
	AssertNoError(t, err)
	AssertZero(t, n)
	b.AppendString("foobar")
	n, err = b.WriteTo(&dest)
	AssertEqual(t, len("foobar"), int(n))
	AssertNoError(t, err)
	AssertEqual(t, "foobar", dest.String())
	AssertZero(t, len(b))
}

func TestBuffer_Reset(t *testing.T) {
	var b buffer
	b.AppendString("foobar")
	AssertEqual(t, "foobar", b.String())
	AssertEqual(t, len("foobar"), len(b))
	bufCap := cap(b)
	b.Reset()
	AssertZero(t, len(b))
	AssertEqual(t, bufCap, cap(b))
}

func TestBuffer_WriteTo_Err(t *testing.T) {
	w := writerFunc(func(b []byte) (int, error) { return 0, errors.New("nope") })
	var b buffer
	b.AppendString("foobar")
	_, err := b.WriteTo(w)
	AssertError(t, err)

	w = writerFunc(func(b []byte) (int, error) { return 0, nil })
	_, err = b.WriteTo(w)
	AssertError(t, err)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Expected io.ErrShortWrite, go %T", err)
	}
}

func BenchmarkBuffer(b *testing.B) {
	data := []byte("foobarbaz")

	b.Run("std", func(b *testing.B) {
		buf := bytes.Buffer{}
		for i := 0; i < b.N; i++ {
			buf.Write(data)
			buf.WriteByte('.')
			buf.Reset()
		}
	})

	b.Run("buffer", func(b *testing.B) {
		buf := buffer{}
		for i := 0; i < b.N; i++ {
			buf.Append(data)
			buf.AppendByte('.')
			buf.Reset()
		}
	})
}
