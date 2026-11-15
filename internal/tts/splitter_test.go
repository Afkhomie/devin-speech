package tts

import "testing"

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Hi. How are you? Fine!", []string{"Hi.", "How are you?", "Fine!"}},
		{"", []string{}},
		{"   ", []string{}},
		{"one long sentence without punctuation that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on", []string{"one long sentence without punctuation that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on"}},
	}
	for _, c := range cases {
		got := SplitSentences(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("SplitSentences(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("SplitSentences(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestSplitLongSentenceCapsLength(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "word "
	}
	parts := SplitSentences(long)
	for _, p := range parts {
		if len(p) > 320 {
			t.Fatalf("sentence too long: %d", len(p))
		}
	}
}

func TestMarkdownStripped(t *testing.T) {
	in := "# Title\n\nHere is `code` and **bold** text.\n\n```\ncode block\n```\n\n- bullet point\n\n[link text](http://x.com) end."
	got := SplitSentences(in)
	joined := ""
	for _, s := range got {
		joined += s + " "
	}
	for _, bad := range []string{"```", "#", "**", "[link text](", "- bullet"} {
		if contains(joined, bad) {
			t.Fatalf("markdown residue %q in %q", bad, joined)
		}
	}
	if len(got) == 0 {
		t.Fatal("no sentences produced")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
