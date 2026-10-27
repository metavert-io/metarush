// Package links builds the dashboard paths where things live, so a job's
// details, an activity row and a notification all open the same place with the
// object selected: a render in its project's Renders tab, a clip in Sources (or
// the inbox), its Speakers tab, a version in History, a QA report on its
// render, the To-dos page filtered to a clip.
package links

import "net/url"

func q(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v.Encode()
}

// Asset opens a clip: its project's Sources tab, or the inbox viewer.
func Asset(projectID, assetID string) string {
	if projectID == "" {
		return "/?" + q("asset", assetID)
	}
	return "/p/" + projectID + "?" + q("tab", "sources", "asset", assetID)
}

// AssetAt opens a clip at a source time with words highlighted in its transcript.
func AssetAt(projectID, assetID, t, highlight string) string {
	if projectID == "" {
		return "/?" + q("asset", assetID, "t", t, "hl", highlight)
	}
	return "/p/" + projectID + "?" + q("tab", "sources", "asset", assetID, "t", t, "hl", highlight)
}

// AssetSpeakers opens a clip on its Speakers tab (who speaks, names, listening samples).
func AssetSpeakers(projectID, assetID string) string {
	if projectID == "" {
		return "/?" + q("asset", assetID, "panel", "speakers")
	}
	return "/p/" + projectID + "?" + q("tab", "sources", "asset", assetID, "panel", "speakers")
}

// AssetOnScreen opens a clip on its On screen tab (what the picture shows, faces, text).
func AssetOnScreen(projectID, assetID string) string {
	if projectID == "" {
		return "/?" + q("asset", assetID, "panel", "onscreen")
	}
	return "/p/" + projectID + "?" + q("tab", "sources", "asset", assetID, "panel", "onscreen")
}

// AssetOnScreenAt opens a clip's On screen tab at a source time.
func AssetOnScreenAt(projectID, assetID, t string) string {
	if projectID == "" {
		return "/?" + q("asset", assetID, "panel", "onscreen", "t", t)
	}
	return "/p/" + projectID + "?" + q("tab", "sources", "asset", assetID, "panel", "onscreen", "t", t)
}

// Person opens a person on the Library's People tab (every video they speak in, D184).
func Person(personID string) string {
	if personID == "" {
		return "/library/people"
	}
	return "/library/people?" + q("person", personID)
}

// Todos opens the To-dos page (questions for people), filtered to one clip
// when assetID is set.
func Todos(assetID string) string {
	if assetID == "" {
		return "/todos"
	}
	return "/todos?" + q("asset", assetID)
}

// Render opens a render, highlighted in its project's Renders tab.
func Render(projectID, renderID string) string {
	if projectID == "" {
		return "/renders/" + renderID
	}
	return "/p/" + projectID + "?" + q("tab", "renders", "render", renderID)
}

// QA opens a render's QA report.
func QA(projectID, renderID, reportID string) string {
	if projectID == "" {
		return "/renders/" + renderID + "?" + q("qa", reportID)
	}
	return "/p/" + projectID + "?" + q("tab", "renders", "render", renderID, "qa", reportID)
}

// Revision opens a version, highlighted in the project's History.
func Revision(projectID, revisionID string) string {
	return "/p/" + projectID + "?" + q("tab", "history", "rev", revisionID)
}

// Project opens a project.
func Project(projectID string) string { return "/p/" + projectID }

// CutSheet opens a version's script (its cut sheet) in the project's Script
// tab (the latest when revisionID is empty). The tab's old key, "sheet", still
// works in links people saved.
func CutSheet(projectID, revisionID string) string {
	return "/p/" + projectID + "?" + q("tab", "script", "rev", revisionID)
}

// CutSheetCompare compares two versions as cut sheets.
func CutSheetCompare(projectID, left, right string) string {
	return "/p/" + projectID + "?" + q("tab", "script", "mode", "compare", "left", left, "right", right)
}

// Job opens a job's details.
func Job(jobID string) string { return "/renders?" + q("tab", "jobs", "job", jobID) }

// Conversation opens a Claude Code conversation in the assistant panel.
func Conversation(projectID, conversationID string) string {
	if projectID == "" {
		return "/?" + q("ask", conversationID)
	}
	return "/p/" + projectID + "?" + q("ask", conversationID)
}

// Log opens the activity log filtered to one object — key is job, render,
// asset, project or conversation — or, with key "entry", one entry.
func Log(key, id string) string {
	if key == "" || id == "" {
		return "/settings/log"
	}
	return "/settings/log?" + q(key, id)
}

// ContentSettings opens a project's Settings tab on its Content Settings
// (D192), at one part (brand, spelling, voice, rules) or at the top. Without
// a project, the default's (what new projects use): the dashboard finds it.
func ContentSettings(projectID, part string) string {
	if projectID == "" {
		if part == "" {
			return "/settings/content"
		}
		return "/settings/content?" + q("part", part)
	}
	if part == "" {
		return "/p/" + projectID + "?" + q("tab", "settings")
	}
	return "/p/" + projectID + "?" + q("tab", "settings", "part", part)
}

// Resource opens a resource: a project's Resources tab with its details, or
// for a workspace resource the default's Content Settings (where the brand
// that uses the workspace library is).
func Resource(projectID, resourceID string) string {
	if projectID == "" {
		return "/settings/brand"
	}
	if resourceID == "" {
		return "/p/" + projectID + "?" + q("tab", "resources")
	}
	return "/p/" + projectID + "?" + q("tab", "resources", "resource", resourceID)
}

// Improvements opens the improvement ideas found in the log.
func Improvements() string { return "/improvements" }

// Motion opens a project motion graphic's review page (source, imports, test
// frames, preview, decisions) — or the list of motion graphics.
func Motion(hash string) string {
	if hash == "" {
		return "/library/motion"
	}
	return "/motion/" + hash
}

// Remotion opens a Remotion source in its project's Sources tab (D131),
// optionally on one version (its review: the change, before/after stills,
// approval).
func Remotion(projectID, sourceID, version string) string {
	return "/p/" + projectID + "?" + q("tab", "sources", "remotion", sourceID, "rv", version)
}

// Ability opens an ability on Library → Abilities (D230), at a version when
// one is given.
func Ability(name, version string) string {
	if version == "" {
		return "/library/abilities?" + q("ability", name)
	}
	return "/library/abilities?" + q("ability", name, "version", version)
}

// AbilityRun opens one run of an ability (D234).
func AbilityRun(name, runID string) string {
	return "/library/abilities?" + q("ability", name, "run", runID)
}

// VoiceoverFit opens the Voiceover tab on Claude's fitted script (the card
// actionID of conversation convID), side by side with the person's own.
func VoiceoverFit(projectID, convID, actionID string) string {
	return "/p/" + projectID + "?" + q("tab", "voiceover", "fit", convID+"~"+actionID)
}

// AutoSpending is where a person sees what Auto spent by itself today (D241).
const AutoSpending = "/settings/preferences#auto-spending"

// Receipt is a project's receipt (D240).
func Receipt(projectID string) string { return "/p/" + projectID + "?" + q("tab", "receipt") }
