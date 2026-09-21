package minimax

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Video generation on MiniMax Agent is a plugin, not a model.
//
// The conversation API has no way to select H3: `GET /minimax-cloud/api/v1/config`
// returns the authoritative model list and it contains only the chat models
// (MiniMax-M3, MiniMax-M2.7, MiniMax-M2.7-highspeed). H3 lives behind the
// `video-creater` plugin, and there is no video endpoint in the web bundle
// either — generation is submitted by the agent's own server-side Connector
// tool (`connector__matrix__submit_video_generation`), which a client never
// calls directly.
//
// What the client *does* control is how the turn is phrased, and that turns out
// to be the entire integration surface:
//
//  1. The composer's reference chip serialises to `@video-creater` in the
//     message text, and the plugin's own skill is described as triggering on
//     that mention. Verified against the real upstream: a plain-text mention
//     routes to the plugin and it answers from its own instructions.
//  2. The generation parameters travel in a trailing
//     `<video-generation-options>` block appended to the same text.
//
// Both are reproduced here. The block's shape is not a guess — it is the pair
// the web bundle uses to write and re-read its own messages, and the two agree:
//
//	writer: `${tag}\n${JSON.stringify(opts)}\n</${tag}>`, preceded by a blank line
//	reader: /(?:^|\r?\n\r?\n)<tag>\r?\n([\s\S]*?)\r?\n<\/tag>(?![\s\S])/u
//
// The reader is anchored to the end of the message, which is why the block is
// always appended last and nothing may follow it.

// VideoOptions are the generation parameters carried in the options block.
//
// Only these four keys are accepted upstream: the bundle's parser drops every
// other key, and drops the whole block if any surviving value is the wrong
// type. Duration must be a positive integer; the rest must be non-empty strings.
type VideoOptions struct {
	Model      string
	Ratio      string
	Resolution string
	Duration   int
}

// videoOptionsTag is the default tag name. It is a parameter rather than a
// constant because the tag belongs to the upstream bundle, and a rename should
// be a settings edit rather than a rebuild.
const videoOptionsTag = "video-generation-options"

// defaultVideoPlugin is the plugin reference the agent routes on.
const defaultVideoPlugin = "video-creater"

// VideoClientIntent is the `client_intent` a video turn carries.
//
// Recovered from the web bundle's own tool-kind table, where all four video
// tools — BatchTextToVideo, BatchImageToVideo, VideosRead, VideosUnderstand —
// map to this one string. The sibling entries in that table (`generate_image`,
// `audio_generation`, `describe_image`) are what make it credible: it is a
// routing vocabulary, not a single magic value. The same bundle declares it as
// an optional field of the message request, so it is part of the protocol
// rather than something inferred from a sample.
//
// What it does *not* have is proof of an effect. It was tried once early on,
// against the entry point that has since turned out to be the wrong one, and
// made no difference there — which isolates nothing. It is sent on every video
// turn because that is what the web client does, not because it has been shown
// to change the outcome.
const VideoClientIntent = "video_generation"

// Normalize fills in whatever the caller left out and rejects what cannot be
// repaired.
//
// Falling back to a default is not a convenience here, it is a requirement. The
// plugin's skill asks the user to confirm every unspecified choice before it
// generates anything, and a headless API call has no user to answer — so an
// omitted parameter does not quietly become an upstream default, it stalls the
// turn. A complete set is what makes the request unattended.
//
// The zero VideoOptions is not usable: the model has no default, because
// guessing which of three differently-priced models to bill would be the wrong
// kind of helpful.
func (o VideoOptions) Normalize(fallback VideoOptions) (VideoOptions, error) {
	out := o
	if strings.TrimSpace(out.Model) == "" {
		out.Model = strings.TrimSpace(fallback.Model)
	}
	if strings.TrimSpace(out.Ratio) == "" {
		out.Ratio = strings.TrimSpace(fallback.Ratio)
	}
	if strings.TrimSpace(out.Resolution) == "" {
		out.Resolution = strings.TrimSpace(fallback.Resolution)
	}
	if out.Duration <= 0 {
		out.Duration = fallback.Duration
	}

	out.Model = strings.TrimSpace(out.Model)
	out.Ratio = strings.TrimSpace(out.Ratio)
	out.Resolution = strings.TrimSpace(out.Resolution)

	if out.Model == "" {
		return VideoOptions{}, fmt.Errorf("video model is required")
	}
	if out.Duration <= 0 {
		return VideoOptions{}, fmt.Errorf("video duration must be a positive whole number of seconds")
	}
	return out, nil
}

// OptionsBlock renders the trailing `<video-generation-options>` block.
//
// Keys are emitted in a fixed order rather than through a map, because a Go map
// would serialise in a random order and produce a different byte string for the
// same request on every call. The upstream parses this as JSON and does not
// care, but a request body that changes shape run to run makes an audit log
// useless for diffing and a cache useless for deduping.
func (o VideoOptions) OptionsBlock(tag string) string {
	if strings.TrimSpace(tag) == "" {
		tag = videoOptionsTag
	}
	pairs := make([]string, 0, 4)
	pairs = append(pairs, `"duration":`+strconv.Itoa(o.Duration))
	if o.Model != "" {
		pairs = append(pairs, `"model":`+jsonString(o.Model))
	}
	if o.Ratio != "" {
		pairs = append(pairs, `"ratio":`+jsonString(o.Ratio))
	}
	if o.Resolution != "" {
		pairs = append(pairs, `"resolution":`+jsonString(o.Resolution))
	}
	return "<" + tag + ">\n{" + strings.Join(pairs, ",") + "}\n</" + tag + ">"
}

// BuildVideoPrompt composes the message text for a video-generation turn.
//
// The plugin mention goes first so the routing decision is made before the
// prompt body is read, and the options block goes last because the upstream's
// own parser only recognises it at the very end of the message.
//
// An existing mention is not duplicated: callers reach this from a chat request
// whose text may already say `@video-creater`, and `@video-creater @video-creater
// make a cat video` is not obviously harmless — the mention is a routing signal,
// not a word.
func BuildVideoPrompt(prompt, plugin string, options VideoOptions, tag string) string {
	if strings.TrimSpace(plugin) == "" {
		plugin = defaultVideoPlugin
	}
	mention := "@" + plugin

	body := strings.TrimSpace(stripOptionsBlock(prompt, tag))
	if !hasMention(body, plugin) {
		if body == "" {
			body = mention
		} else {
			body = mention + " " + body
		}
	}
	return body + "\n\n" + options.OptionsBlock(tag)
}

// hasMention reports whether the text already references the plugin.
//
// The comparison is case-insensitive and anchored on a non-word character
// afterwards, so `@video-creater` matches and `@video-creaters` does not. Being
// strict here only costs a duplicated prefix; being loose would silently drop a
// reference the caller deliberately wrote.
func hasMention(text, plugin string) bool {
	lower := strings.ToLower(text)
	needle := "@" + strings.ToLower(plugin)
	for offset := 0; ; {
		index := strings.Index(lower[offset:], needle)
		if index < 0 {
			return false
		}
		end := offset + index + len(needle)
		if end >= len(lower) || !isWordByte(lower[end]) {
			return true
		}
		offset = end
	}
}

func isWordByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_':
		return true
	}
	return false
}

// stripOptionsBlock removes a trailing options block.
//
// Without it, a client that resends its own history would stack a second block
// onto the first, and the upstream's parser — which is anchored to the end of
// the message and takes the first match — would keep reading the stale one.
func stripOptionsBlock(text, tag string) string {
	if strings.TrimSpace(tag) == "" {
		tag = videoOptionsTag
	}
	closing := "</" + tag + ">"
	trimmed := strings.TrimRight(text, " \t\r\n")
	if !strings.HasSuffix(trimmed, closing) {
		return text
	}
	opening := "<" + tag + ">"
	index := strings.LastIndex(trimmed, opening)
	if index < 0 {
		return text
	}
	return trimmed[:index]
}

// jsonString encodes a string as a JSON literal.
//
// json.Marshal on a plain string cannot fail, but it can escape characters the
// upstream might not round-trip; nothing here needs to be cleverer than the
// standard encoder, so it is used directly and the error is folded into a
// quoted fallback rather than propagated.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
