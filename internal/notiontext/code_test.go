package notiontext

import (
	"encoding/json"
	"testing"
)

func TestCodeTextPreservesSegmentWhitespace(t *testing.T) {
	var body any
	if err := json.Unmarshal([]byte(`{"rich_text":[{"plain_text":"\tfirst\n"},{"text":{"content":"  second  \n\n"}}]}`), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := CodeText(body), "\tfirst\n  second  \n\n"; got != want {
		t.Fatalf("CodeText = %q; want %q", got, want)
	}
}
