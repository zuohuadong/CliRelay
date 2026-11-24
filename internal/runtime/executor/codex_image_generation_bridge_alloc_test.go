package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripCodexHistoryDataURLImages_LargeBodyAllocBound(t *testing.T) {
	// Multi-MB assistant history with one huge data URL — old path did N sjson copies of whole body.
	b64 := strings.Repeat("A", 2*1024*1024) // ~2MB base64
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.4","input":[`)
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if i == 3 {
			b.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done\n\n![img](data:image/png;base64,`)
			b.WriteString(b64)
			b.WriteString(`)"}]}`)
			continue
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		b.WriteString(`{"type":"message","role":"`)
		b.WriteString(role)
		b.WriteString(`","content":[{"type":"input_text","text":"`)
		b.WriteString(strings.Repeat("x", 4096))
		b.WriteString(`"}]}`)
	}
	b.WriteString(`]}`)
	in := []byte(b.String())

	// Correctness once (reattach may keep one structured image; assistant text must be stripped).
	out := stripCodexHistoryDataURLImages(in)
	asst := gjson.GetBytes(out, "input.3.content.0.text").String()
	if strings.Contains(asst, "base64,") {
		t.Fatalf("assistant text still has base64; len=%d", len(asst))
	}
	if !strings.Contains(asst, "cliproxy-image:") {
		t.Fatalf("placeholder missing: %q", asst)
	}

	res := testing.Benchmark(func(bb *testing.B) {
		bb.ReportAllocs()
		bb.SetBytes(int64(len(in)))
		for i := 0; i < bb.N; i++ {
			_ = stripCodexHistoryDataURLImages(in)
		}
	})
	if res.N == 0 {
		t.Fatal("benchmark did not run")
	}
	avg := res.AllocedBytesPerOp()
	// Rebuild + extract + reattach keep a few body-sized buffers (image lives in lastImage +
	// structured input_image). Bound is intentionally loose vs old N×full-body sjson copies.
	if avg > int64(len(in))*20 {
		t.Fatalf("alloc/op=%d body=%d ratio=%.1f want <=20x", avg, len(in), float64(avg)/float64(len(in)))
	}
	t.Logf("body=%dB alloc/op=%d N=%d ratio=%.2f", len(in), avg, res.N, float64(avg)/float64(len(in)))
}
