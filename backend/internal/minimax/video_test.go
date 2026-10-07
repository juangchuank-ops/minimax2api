package minimax

import (
	"encoding/json"
	"regexp"
	"strconv"
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

// ------------------------------------------------------------- model ranges

// The ranges the two H3 variants advertise, as the store seeds them. They are
// written out rather than imported so that a change to the catalogue has to be
// a change here too: the numbers are a claim about the upstream, and the test
// is where the claim is recorded.
var (
	testRatios      = []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}
	testDurations   = []int{5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	testH3MaxLimits = VideoLimits{Ratios: testRatios, Resolutions: []string{"480P", "768P"}, Durations: testDurations}
	testH3Limits    = VideoLimits{Ratios: testRatios, Resolutions: []string{"768P", "2K"}, Durations: testDurations}
)

// Every value a model advertises has to survive untouched.
//
// The whole matrix, because "supported" is the claim being made: 11 durations
// times 6 ratios times each model's resolutions. A repair on any of them would
// mean the gateway quietly changed a request it had no business changing, and
// since the repaired value is still a valid one, nothing downstream would look
// wrong.
func TestConformLeavesEveryAdvertisedValueAlone(t *testing.T) {
	cases := []struct {
		model  string
		limits VideoLimits
	}{
		{"MiniMax-H3-Max", testH3MaxLimits},
		{"MiniMax-H3", testH3Limits},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			fallback := VideoOptions{Model: tc.model, Ratio: "16:9", Resolution: "768P", Duration: 5}
			for _, ratio := range tc.limits.Ratios {
				for _, resolution := range tc.limits.Resolutions {
					for _, duration := range tc.limits.Durations {
						want := VideoOptions{
							Model: tc.model, Ratio: ratio, Resolution: resolution, Duration: duration,
						}
						got, adjustments := want.Conform(tc.limits, fallback)
						if got != want {
							t.Errorf("ratio %s / %s / %ds became %+v", ratio, resolution, duration, got)
						}
						if len(adjustments) != 0 {
							t.Errorf("ratio %s / %s / %ds was reported as adjusted: %+v",
								ratio, resolution, duration, adjustments)
						}
					}
				}
			}
		})
	}
}

// A value the model does not offer moves to the nearest one it does.
//
// Nearest rather than "the default" or "the first entry", because the axes are
// ordered and the caller's intent survives the move: someone who asked for the
// sharpest resolution available anywhere is asking for the sharpest this model
// has, not for the cheapest.
func TestConformMovesToTheNearestAcceptedValue(t *testing.T) {
	fallback := VideoOptions{Model: "MiniMax-H3-Max", Ratio: "16:9", Resolution: "768P", Duration: 5}
	cases := []struct {
		name   string
		field  string
		limits VideoLimits
		want   VideoOptions
		got    VideoOptions
		used   string
	}{
		{
			name: "2K on a model that stops at 768P", field: "resolution",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "16:9", Resolution: "2K", Duration: 5},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}, used: "768P",
		},
		{
			// The other model's floor, on this model's ceiling: 480P is what
			// H3-Max offers, so the same request is accepted there and moved
			// here.
			name: "480P on a model that starts at 768P", field: "resolution",
			limits: testH3Limits,
			want:   VideoOptions{Ratio: "16:9", Resolution: "480P", Duration: 5},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}, used: "768P",
		},
		{
			name: "a resolution between the two", field: "resolution",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "16:9", Resolution: "1080P", Duration: 5},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}, used: "768P",
		},
		{
			name: "an aspect between 4:3 and 16:9", field: "ratio",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "1.85:1", Resolution: "768P", Duration: 5},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}, used: "16:9",
		},
		{
			name: "a portrait aspect", field: "ratio",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "3:5", Resolution: "768P", Duration: 5},
			got:    VideoOptions{Ratio: "9:16", Resolution: "768P", Duration: 5}, used: "9:16",
		},
		{
			name: "longer than the panel offers", field: "duration",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 30},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 15}, used: "15",
		},
		{
			name: "shorter than the panel offers", field: "duration",
			limits: testH3MaxLimits,
			want:   VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 2},
			got:    VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}, used: "5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, adjustments := tc.want.Conform(tc.limits, fallback)
			if got != tc.got {
				t.Fatalf("got %+v, want %+v", got, tc.got)
			}
			if len(adjustments) != 1 {
				t.Fatalf("expected exactly one adjustment, got %+v", adjustments)
			}
			adjustment := adjustments[0]
			if adjustment.Field != tc.field || adjustment.Used != tc.used {
				t.Fatalf("adjustment = %+v, want field %s used %s", adjustment, tc.field, tc.used)
			}
			// The requested value is echoed back, because a caller that gets a
			// 768P video after asking for 2K needs to know it asked for 2K.
			requested := map[string]string{
				"ratio":      tc.want.Ratio,
				"resolution": tc.want.Resolution,
				"duration":   strconv.Itoa(tc.want.Duration),
			}[tc.field]
			if adjustment.Requested != requested {
				t.Fatalf("requested = %q, want %q", adjustment.Requested, requested)
			}
		})
	}
}

// An unreadable value has no distance to measure, so the console default is the
// next best answer — and the first entry is the best available when even the
// default is not offered.
func TestConformFallsBackWhenAValueCannotBeRanked(t *testing.T) {
	fallback := VideoOptions{Ratio: "16:9", Resolution: "768P", Duration: 5}

	got, adjustments := (VideoOptions{Ratio: "wide", Resolution: "auto", Duration: 5}).
		Conform(testH3Limits, fallback)
	if got.Ratio != "16:9" || got.Resolution != "768P" {
		t.Fatalf("got %+v, want the defaults", got)
	}
	if len(adjustments) != 2 {
		t.Fatalf("expected two adjustments, got %+v", adjustments)
	}

	// The default itself is not in the set: nothing can be measured and nothing
	// can be inherited, so the first entry is used rather than nothing at all.
	narrow := VideoLimits{Ratios: []string{"1:1"}, Resolutions: []string{"2K"}, Durations: []int{10}}
	got, _ = (VideoOptions{Ratio: "", Resolution: "auto", Duration: 5}).Conform(narrow, fallback)
	if got.Ratio != "1:1" || got.Resolution != "2K" || got.Duration != 10 {
		t.Fatalf("got %+v, want the only accepted values", got)
	}
}

// A model with no recorded ranges is left alone entirely.
//
// That is Hailuo 2.3 today: its parameter panel has not been read, and a range
// invented from the H3 variants would reject requests the upstream may well
// accept. Passing an out-of-range value through costs a failed turn at worst;
// rejecting a valid one costs a turn that would have worked.
func TestConformDoesNotConstrainAnUnreadModel(t *testing.T) {
	want := VideoOptions{Model: "MiniMax-Hailuo-2.3", Ratio: "5:4", Resolution: "1080P", Duration: 25}
	got, adjustments := want.Conform(VideoLimits{}, VideoOptions{})
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(adjustments) != 0 {
		t.Fatalf("an unconstrained model reported adjustments: %+v", adjustments)
	}
}

// A tie keeps the earlier entry, which is the order the upstream panel shows.
// Nothing in the real catalogue ties — the durations are contiguous — but a
// rule that is not pinned is a rule that changes by accident.
func TestConformBreaksATieTowardsTheEarlierEntry(t *testing.T) {
	limits := VideoLimits{Durations: []int{5, 15}}
	got, adjustments := (VideoOptions{Duration: 10}).Conform(limits, VideoOptions{})
	if got.Duration != 5 {
		t.Fatalf("duration = %d, want the earlier entry 5", got.Duration)
	}
	if len(adjustments) != 1 || adjustments[0].Used != "5" {
		t.Fatalf("adjustments = %+v", adjustments)
	}
}
