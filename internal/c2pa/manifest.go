package c2pa

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// What a manifest says (D419), checked against C2PA 2.5 and c2pa-rs 0.91.6:
// MetaRush made the video (the claim generator, with its version); it was
// created (c2pa.created, first) as a composite of the parts it lists —
// footage, sound, pictures, each a componentOf ingredient named by its
// SHA-256 (instanceID "sha256:…", MetaRush's own identifier, as C2PA lets a
// claim generator choose one for an asset without one) and a neutral title
// ("Footage 1"), never a file, project, client or person's name, a prompt or
// a consent; each part placed into it (c2pa.placed) and the whole edited
// (c2pa.edited), every action MetaRush took (allActionsIncluded). Actions and
// ingredients are created assertions: MetaRush vouches for them. Parts an AI
// model made or changed carry the IPTC digital source type that says so, on
// the ingredient and its placing. No thumbnail, nothing of the pictures. Who
// published it is the signing certificate's to say (C2PA's identity
// recommendation): the deprecated schema.org CreativeWork assertion isn't
// written.

// IPTC's digital source types (cv.iptc.org/newscodes/digitalsourcetype/).
const iptc = "http://cv.iptc.org/newscodes/digitalsourcetype/"

const (
	// SourceTrained is "Created using Generative AI": a part an AI model made
	// (generated video, pictures, speech, music, a presenter).
	SourceTrained = iptc + "trainedAlgorithmicMedia"
	// SourceAIEdited is "Edited using Generative AI": captured media an AI
	// model changed by making new content in it (a mouth matched to new
	// words, a voice changed).
	SourceAIEdited = iptc + "compositeWithTrainedAlgorithmicMedia"
	// SourceEnhanced is "Algorithmically-altered media": changed by an
	// algorithm without changing its main content (a picture's background
	// taken away, a picture made larger).
	SourceEnhanced = iptc + "algorithmicallyEnhanced"
	// SourceSynthetic is "Composite including generative AI elements": the
	// video, when a generative AI model made or edited any part of it.
	SourceSynthetic = iptc + "compositeSynthetic"
	// SourceComposite is a composite of several elements: the video otherwise.
	SourceComposite = iptc + "composite"
)

// How AI took part in a part (Part.AI): models' one answer (D435).
const (
	AINone     = ""
	AIMade     = models.AIWorkMade     // a generative AI model made it
	AIEdited   = models.AIWorkEdited   // a generative AI model changed captured media
	AIEnhanced = models.AIWorkEnhanced // an AI model changed it, making nothing new
)

// Part is one thing a video is made of.
type Part struct {
	// What it is, for its title: footage, sound, music, picture, document,
	// voice, words (new words in someone's own voice), presenter, lip-sync,
	// sound-effect, voice-change, dubbing.
	What   string
	SHA256 string
	// Format is its media type ("video/mp4"); "" unknown.
	Format string
	AI     string
	// Made says what made it, for an AI part: the service and model
	// ("Google, veo-3.1"); never a prompt.
	Made string
}

// Manifest is what MetaRush says about a delivered video.
type Manifest struct {
	// Version is MetaRush's.
	Version string
	Parts   []Part
}

// Title is the title every manifest has: never the file's name (c2patool
// would use it, and a delivered file is named after its project).
const Title = "Video"

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxParts keeps a manifest small: parts past it are counted, not listed.
const MaxParts = 200

// sourceOf is the digital source type of a part AI took part in ("" none).
func sourceOf(ai string) string {
	switch ai {
	case AIMade:
		return SourceTrained
	case AIEdited:
		return SourceAIEdited
	case AIEnhanced:
		return SourceEnhanced
	}
	return ""
}

// Definition is the manifest definition c2patool reads (its JSON: title,
// claim_generator_info, ingredients and assertions; no signing fields — the
// signer says those).
func (m Manifest) Definition() ([]byte, error) {
	agent := map[string]any{"name": "MetaRush"}
	if v := strings.TrimSpace(m.Version); v != "" {
		agent["version"] = v
	}
	var ingredients []any
	actions := []any{map[string]any{"action": "c2pa.created", "digitalSourceType": m.SourceType(), "softwareAgent": agent}}
	count := map[string]int{}
	for i, p := range m.listed() {
		word := partWord(p)
		count[word]++
		label := fmt.Sprintf("part-%d", i+1)
		ing := map[string]any{"title": fmt.Sprintf("%s %d", word, count[word]), "relationship": "componentOf",
			"instance_id": "sha256:" + p.SHA256, "label": label}
		if f := strings.TrimSpace(p.Format); f != "" && strings.Contains(f, "/") && !strings.ContainsAny(f, " \t\r\n") {
			ing["format"] = f
		}
		placed := map[string]any{"action": "c2pa.placed", "softwareAgent": agent, "parameters": map[string]any{"ingredientIds": []string{label}}}
		if src := sourceOf(p.AI); src != "" {
			ing["digital_source_type"] = src
			placed["digitalSourceType"] = src
			if made := cleanName(p.Made); made != "" {
				ing["description"] = map[string]string{AIMade: "Made", AIEdited: "Edited", AIEnhanced: "Changed"}[p.AI] + " with " + made
			}
		}
		ingredients = append(ingredients, ing)
		actions = append(actions, placed)
	}
	actions = append(actions, map[string]any{"action": "c2pa.edited", "softwareAgent": agent})
	def := map[string]any{
		"title":                Title,
		"claim_generator_info": []any{agent},
		"assertions": []any{map[string]any{"label": "c2pa.actions", "created": true,
			"data": map[string]any{"allActionsIncluded": true, "actions": actions}}},
	}
	if len(ingredients) > 0 {
		def["ingredients"] = ingredients
	}
	return json.MarshalIndent(def, "", "  ")
}

// listed are the parts the manifest names: each SHA-256 once, at most MaxParts.
func (m Manifest) listed() []Part {
	seen := map[string]bool{}
	var out []Part
	for _, p := range m.Parts {
		if !hexRe.MatchString(p.SHA256) || seen[p.SHA256] {
			continue
		}
		seen[p.SHA256] = true
		if len(out) < MaxParts {
			out = append(out, p)
		}
	}
	return out
}

// SourceType is the video's own digital source type: a composite including
// generative AI when a generative AI model made or edited any part of it.
func (m Manifest) SourceType() string {
	for _, p := range m.Parts {
		if p.AI == AIMade || p.AI == AIEdited {
			return SourceSynthetic
		}
	}
	return SourceComposite
}

// Counts says how many parts the manifest lists, and of them how many a
// generative AI model made or edited, and how many AI changed otherwise.
func (m Manifest) Counts() (parts, generative, changed int) {
	for _, p := range m.listed() {
		parts++
		switch p.AI {
		case AIMade, AIEdited:
			generative++
		case AIEnhanced:
			changed++
		}
	}
	return parts, generative, changed
}

// partWord is a part's neutral title word.
func partWord(p Part) string {
	switch p.AI {
	case AIEnhanced:
		switch p.What {
		case "picture":
			return "Picture changed with AI"
		case "footage":
			return "Footage changed with AI" // a clip's background replaced (D511)
		case "sound", "voice":
			return "Sound cleaned up with AI"
		}
		return "Part changed with AI"
	case AIEdited:
		switch p.What {
		case "lip-sync", "footage":
			return "Footage edited with AI"
		case "voice-change":
			return "Voice changed with AI"
		case "picture":
			return "Picture edited with AI"
		case "sound", "voice":
			return "Sound edited with AI"
		}
		return "Part edited with AI"
	case AIMade:
		switch p.What {
		case "footage", "video":
			return "AI-generated video"
		case "picture":
			return "AI-generated picture"
		case "voice":
			return "AI voice"
		case "words":
			return "Words said with AI"
		case "presenter":
			return "AI presenter"
		case "music":
			return "AI music"
		case "sound-effect":
			return "AI sound effect"
		case "dubbing":
			return "AI dub"
		case "sound":
			return "AI-generated sound"
		}
		return "AI-generated part"
	}
	switch p.What {
	case "footage", "video":
		return "Footage"
	case "picture":
		return "Picture"
	case "music":
		return "Music"
	case "document":
		return "Document page"
	}
	return "Sound"
}
