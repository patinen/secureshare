package shares

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestToken(t *testing.T) {
	a, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(decoded) < 32 || a == b {
		t.Fatal("insufficient random token")
	}
	if !bytes.Equal(Hash(a), Hash(a)) || bytes.Equal(Hash(a), Hash(b)) {
		t.Fatal("incorrect hash")
	}
}
func TestValidation(t *testing.T) {
	now := time.Now()
	one := 1
	zero := 0
	high := 1001
	long := strings.Repeat("x", 151)
	valid := Input{Type: "TEXT", Text: "hello", ExpiresAt: now.Add(time.Hour), MaxRedemptions: &one}
	cases := []struct {
		name   string
		mutate func(*Input)
		ok     bool
	}{
		{"valid", func(i *Input) {}, true}, {"unlimited", func(i *Input) { i.MaxRedemptions = nil }, true}, {"empty", func(i *Input) { i.Text = "" }, false}, {"whitespace", func(i *Input) { i.Text = " \n\t" }, false}, {"oversized", func(i *Input) { i.Text = strings.Repeat("a", 102401) }, false}, {"exact_size", func(i *Input) { i.Text = strings.Repeat("a", 102400) }, true}, {"past", func(i *Input) { i.ExpiresAt = now.Add(-time.Second) }, false}, {"over_30_days", func(i *Input) { i.ExpiresAt = now.Add(31 * 24 * time.Hour) }, false}, {"zero_limit", func(i *Input) { i.MaxRedemptions = &zero }, false}, {"high_limit", func(i *Input) { i.MaxRedemptions = &high }, false}, {"long_title", func(i *Input) { i.Title = &long }, false}, {"file_type", func(i *Input) { i.Type = "FILE" }, false}, {"nul", func(i *Input) { i.Text = "hello\x00" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := valid
			c.mutate(&in)
			if (Validate(in, now) == nil) != c.ok {
				t.Fatal("wrong validation result")
			}
		})
	}
}
