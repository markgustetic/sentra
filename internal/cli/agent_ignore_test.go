package cli

import (
	"bytes"
	"encoding/json"
	"testing"

)

func TestWriteIgnoreAdviceJSON_NilIsEmptyArray(t *testing.T) {
	var out bytes.Buffer
	if err := writeIgnoreAdviceJSON(&out, nil); err != nil {
		t.Fatalf("writeIgnoreAdviceJSON: %v", err)
	}
	if out.String() != "[]\n" {
		t.Fatalf("output = %q, want empty JSON array", out.String())
	}
}

func TestWriteIgnoreAdviceJSON_RoundTripsAdvice(t *testing.T) {
	advice := []ignoreAdvice{
		{
			Pattern:  "node_modules/",
			Category: "cache_dirs",
			Target:   "node_modules",
			Reason:   "regenerable cache/build directory",
		},
		{
			Pattern:  "big.bin",
			Category: "large_files",
			Target:   "/repo/big.bin",
			Reason:   "large file; review whether it belongs in encrypted backups",
			Size:     123,
		},
	}

	var out bytes.Buffer
	if err := writeIgnoreAdviceJSON(&out, advice); err != nil {
		t.Fatalf("writeIgnoreAdviceJSON: %v", err)
	}
	var got []ignoreAdvice
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Pattern != "node_modules/" || got[1].Size != 123 {
		t.Fatalf("decoded advice = %+v", got)
	}
}
