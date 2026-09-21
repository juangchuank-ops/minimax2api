package minimax

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// The block the upstream writes and the block this package writes have to be
// byte-compatible, and the only way to know that is to run the upstream's own
// reader against our writer.
//
// The reader is transcribed from the web bundle:
//
//	let C = RegExp(`(?:^|\\r?\\n\\r?\\n)<${r}>\\r?\\n([\\s\\S]*?)\\r?\\n<\\/${r}>(?![\\s\\S])`, "u")
//
// with one deliberate change: the trailing `(?![\s\S])` lookahead is dropped,
// because Go's regexp is RE2 and has no lookaround at all. That lookahead is
// what makes the reader find only a *trailing* block, so removing it silently
// changes what is being tested — hence `mustParseAsTrailingBlock` below, which
// asserts the same thing the lookahead did. Transcribing the rest verbatim is
// the point: a paraphrase would encode what this package *believes* the format
// is, and the belief is exactly what is under test.
var upstreamOptionsReader = regexp.MustCompile(
	`(?:^|\r?\n\r?\n)<video-generation-options>\r?\n([\s\S]*?)\r?\n</video-generation-options>`,
)

// mustParseAsTrailingBlock applies the upstream reader and then the condition
// the lookahead it lost would have imposed: the block has to end the message.
func mustParseAsTrailingBlock(t *testing.T, text string) string {
	t.Helper()
	match := upstreamOptionsReader.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("the upstream parser does not recognise our block:\n%s", text)
	}
	// The bundle anchors on the end of the whole string. Anything after the
	// closing tag means a real client would not have found the block either.
	tail := text[strings.Index(text, match[0])+len(match[0]):]
	if strings.TrimSpace(tail) != "" {
		t.Fatalf("the upstream parser is anchored to the end, but %q follows the block:\n%s", tail, text)
	}
	return match[1]
}

func TestBuildVideoPromptIsReadableByTheUpstreamParser(t *testing.T) {
	options := VideoOptions{Model: "MiniMax-H3-Max", Ratio: "16:9", Resolution: "768P", Duration: 5}
	text := BuildVideoPrompt("一只猫在弹钢琴", "video-creater", options, "video-generation-options")

	var parsed VideoOptions
	if err := json.Unmarshal([]byte(mustParseAsTrailingBlock(t, text)), &parsed); err != nil {
		t.Fatalf("block is not valid JSON: %v\n%s", err, text)
	}
	if parsed != options {
		t.Fatalf("round trip lost data: got %+v want %+v", parsed, options)
	}
}

// The reader is anchored to the end of the message, so anything appended after
// the block makes it invisible — the request would look well-formed and generate
// nothing.
func TestOptionsBlockIsAlwaysLast(t *testing.T) {
	text := BuildVideoPrompt("prompt", "video-creater",
		VideoOptions{Model: "MiniMax-H3", Duration: 5}, "video-generation-options")
	mustParseAsTrailingBlock(t, text)
}

// A caller that resends its own history would otherwise stack blocks, and the
// upstream's reader takes the first match — so the stale one would win.
func TestBuildVideoPromptReplacesAnExistingBlock(t *testing.T) {
	first := BuildVideoPrompt("cat", "video-creater",
		VideoOptions{Model: "MiniMax-H3", Duration: 5}, "video-generation-options")
	second := BuildVideoPrompt(first, "video-creater",
		VideoOptions{Model: "MiniMax-H3-Max", Duration: 8}, "video-generation-options")

	if strings.Count(second, "<video-generation-options>") != 1 {
		t.Fatalf("expected exactly one block, got:\n%s", second)
	}
	if !strings.Contains(second, `"model":"MiniMax-H3-Max"`) {
		t.Fatalf("new options did not replace the old ones:\n%s", second)
	}
	if !upstreamOptionsReader.MatchString(second) {
		t.Fatalf("reader rejected the text:\n%s", second)
	}
}

func TestBuildVideoPromptDoesNotRepeatTheMention(t *testing.T) {
	for name, prompt := range map[string]string{
		"exact":      "@video-creater make a cat video",
		"case":       "@Video-Creater make a cat video",
		"mid-text":   "please use @video-creater for this",
		"uppercase":  "@VIDEO-CREATER make a cat video",
		"with-block": "@video-creater cat\n\n<video-generation-options>\n{\"duration\":5}\n</video-generation-options>",
	} {
		text := BuildVideoPrompt(prompt, "video-creater",
			VideoOptions{Model: "MiniMax-H3", Duration: 5}, "video-generation-options")
		if strings.Count(strings.ToLower(text), "@video-creater") != 1 {
			t.Fatalf("%s: mention repeated:\n%s", name, text)
		}
	}
}

// A prefix match must not be mistaken for a reference: `@video-creaters` is a
// different plugin name, and dropping the real mention would send the turn to
// no plugin at all.
func TestBuildVideoPromptAddsTheMentionWhenOnlyAPrefixMatches(t *testing.T) {
	text := BuildVideoPrompt("@video-creaters is not this plugin", "video-creater",
		VideoOptions{Model: "MiniMax-H3", Duration: 5}, "video-generation-options")
	if !strings.HasPrefix(text, "@video-creater @video-creaters") {
		t.Fatalf("mention not added in front of a near-miss:\n%s", text)
	}
}

func TestBuildVideoPromptAddsTheMentionToAnEmptyPrompt(t *testing.T) {
	text := BuildVideoPrompt("   ", "video-creater",
		VideoOptions{Model: "MiniMax-H3", Duration: 5}, "video-generation-options")
	if !strings.HasPrefix(text, "@video-creater\n\n") {
		t.Fatalf("empty prompt did not become a bare mention:\n%q", text)
	}
}

// Key order is fixed rather than taken from a map so the same request produces
// the same bytes twice.
func TestOptionsBlockKeyOrderIsStable(t *testing.T) {
	options := VideoOptions{Model: "MiniMax-H3", Ratio: "16:9", Resolution: "768P", Duration: 5}
	first := options.OptionsBlock("video-generation-options")
	for i := 0; i < 50; i++ {
		if again := options.OptionsBlock("video-generation-options"); again != first {
			t.Fatalf("block is not deterministic:\n%s\n%s", first, again)
		}
	}
	want := "<video-generation-options>\n" +
		`{"duration":5,"model":"MiniMax-H3","ratio":"16:9","resolution":"768P"}` +
		"\n</video-generation-options>"
	if first != want {
		t.Fatalf("unexpected block:\ngot  %s\nwant %s", first, want)
	}
}

// Duration has no sensible default — the upstream bills by it — so an absent
// one is an error rather than a guess.
func TestVideoOptionsRequireAModelAndADuration(t *testing.T) {
	fallback := VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}

	if _, err := (VideoOptions{Duration: 5}).Normalize(fallback); err == nil {
		t.Fatal("a missing model must be rejected, not defaulted")
	}
	if _, err := (VideoOptions{Model: "MiniMax-H3"}).Normalize(VideoOptions{Ratio: "16:9"}); err == nil {
		t.Fatal("a missing duration with no fallback must be rejected")
	}

	filled, err := (VideoOptions{Model: "MiniMax-H3"}).Normalize(fallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filled.Ratio != "16:9" || filled.Resolution != "768P" || filled.Duration != 5 {
		t.Fatalf("fallback not applied: %+v", filled)
	}
}

// A negative or fractional duration is not a duration. The upstream parser
// drops the whole block when a value has the wrong type, so sending one would
// silently turn a generation request into an unanswered question.
func TestVideoOptionsRejectNonPositiveDurations(t *testing.T) {
	for _, duration := range []int{-1, 0} {
		options := VideoOptions{Model: "MiniMax-H3", Duration: duration}
		if _, err := options.Normalize(VideoOptions{}); err == nil {
			t.Fatalf("duration %d was accepted", duration)
		}
	}
}
