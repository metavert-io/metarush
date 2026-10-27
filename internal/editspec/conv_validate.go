package editspec

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// conversationIssues checks the pinned conversations (their members and
// recordings) and the conversation clips (speed, shots, muted members).
func conversationIssues(s *Spec, facts Facts, clips []PlanClip) []Issue {
	var out []Issue
	used := map[string]bool{}
	for _, c := range s.Clips {
		if n := ConvName(c.AssetID); n != "" {
			used[n] = true
		}
	}
	for _, name := range sortedConvNames(s) {
		pin := s.Conversations[name]
		p := "/conversations/" + name
		if !ConversationNamePattern.MatchString(name) {
			out = append(out, errI("bad_name", p, "%q can't be a conversation's short name (lowercase letters, digits, - and _, starting with a letter)", name))
		}
		if !used[name] {
			out = append(out, warnI("conversation_unused", p, "the conversation %q is pinned but no clip plays it", name))
		}
		ids := map[string]bool{}
		sounds := 0
		for i, m := range pin.Members {
			mp := fmt.Sprintf("%s/members/%d", p, i)
			if ids[m.ID] {
				out = append(out, errI("duplicate_id", mp+"/id", "two members of %q are called %q", name, m.ID))
			}
			ids[m.ID] = true
			if strings.TrimSpace(m.Name) == "" {
				out = append(out, errI("conversation_member", mp+"/name", "member %s of %q needs a name (shown in name tags and captions)", m.ID, name))
			}
			switch m.Use {
			case "", UseBoth, UseSound, UsePicture:
			default:
				out = append(out, errI("conversation_member", mp+"/use", "member %s: use is both, sound or picture, not %q", m.ID, m.Use))
			}
			a, ok := facts.Asset(m.AssetID)
			switch {
			case !ok:
				out = append(out, errI("missing_asset", mp+"/assetId", "member %s of %q is a recording that isn't in this workspace (%s)", m.ID, name, m.AssetID))
				continue
			case a.State != "available":
				out = append(out, errI("asset_not_ready", mp+"/assetId", "%s (%s's recording) is %s, not available", a.Name, m.Name, a.State))
			}
			if !a.InProject {
				out = append(out, errI("asset_not_in_project", mp+"/assetId", "%s's recording (%s) is not part of this project; add it to the project first", m.Name, a.Name))
			}
			if m.UseOf() != UseSound && !a.HasVideo {
				out = append(out, warnI("conversation_member", mp+"/use", "%s's recording (%s) has no picture; it is used for its sound (use: sound)", m.Name, a.Name))
			}
			if m.UseOf() != UsePicture && !a.HasAudio {
				out = append(out, warnI("conversation_member", mp+"/use", "%s's recording (%s) has no sound", m.Name, a.Name))
			}
			if m.HasSound() && a.HasAudio {
				sounds++
			}
			if c := m.Crop; c != nil && (c.Left+c.Right >= 0.8 || c.Top+c.Bottom >= 0.8) {
				out = append(out, errI("bad_crop", mp+"/crop", "the crop of %s's picture leaves too little of it (keep at least a fifth across and down)", m.Name))
			}
		}
		if sounds == 0 && len(pin.Members) > 0 {
			out = append(out, warnI("conversation_silent", p, "no member of %q gives sound: the conversation plays silently", name))
		}
	}
	for i, c := range s.Clips {
		name := ConvName(c.AssetID)
		if name == "" {
			continue
		}
		pin, ok := s.Conversations[name]
		if !ok {
			continue // conversation_unknown (Validate)
		}
		p := fmt.Sprintf("/clips/%d", i)
		if c.Speed != 0 && c.Speed != 1 {
			out = append(out, errI("conversation_speed", p+"/speed", "clip %s plays a conversation, which plays at its own speed; it can't be sped up or slowed down", c.ID))
		}
		if c.Framing != nil {
			out = append(out, warnI("conversation_framing", p+"/framing", "clip %s plays a conversation: each person's framing is set on the member (conversations.%s.members), so the clip's framing is ignored", c.ID, name))
		}
		for _, id := range c.MutedMembers {
			if _, ok := pin.Member(id); !ok {
				out = append(out, errI("conversation_member", p+"/mutedMembers", "clip %s leaves out the sound of %q, who isn't in the conversation", c.ID, id))
			}
		}
		fps := float64(s.Output.FPS)
		for j, sh := range c.Shots {
			sp := fmt.Sprintf("%s/shots/%d", p, j)
			if j > 0 && math.Abs(sh.At-c.Shots[j-1].At) < 1e-9 {
				out = append(out, errI("shot_order", sp+"/at", "clip %s has two shots starting at %s; keep one", c.ID, Clock(sh.At)))
			}
			switch sh.Kind() {
			case "":
				out = append(out, errI("shot_kind", sp, "a shot shows exactly one of: a person, a layout, a card or a cutaway (clip %s at %s)", c.ID, Clock(sh.At)))
				continue
			case "member":
				m, ok := pin.Member(sh.Member)
				switch {
				case !ok:
					out = append(out, errI("conversation_member", sp+"/member", "clip %s shows %q at %s, who isn't in the conversation", c.ID, sh.Member, Clock(sh.At)))
				case !m.HasPicture():
					out = append(out, warnI("member_no_picture", sp+"/member", "%s has no picture (sound only); their name shows on a card instead", m.Name))
				default:
					out = append(out, notRecording(m, sh, c, facts, sp)...)
				}
			case "layout":
				if !slices.Contains(Layouts, sh.Layout) {
					out = append(out, errI("layout_unknown", sp+"/layout", "layout is side-by-side, stacked, pip or grid, not %q", sh.Layout))
					continue
				}
				ids := sh.Members
				if len(ids) == 0 {
					ids = pin.PictureMembers()
				}
				n := 0
				for _, id := range ids {
					m, ok := pin.Member(id)
					switch {
					case !ok:
						out = append(out, errI("conversation_member", sp+"/members", "clip %s's layout at %s names %q, who isn't in the conversation", c.ID, Clock(sh.At), id))
					case !m.HasPicture():
						out = append(out, errI("layout_members", sp+"/members", "%s has no picture (sound only), so they can't be in a layout", m.Name))
					default:
						n++
					}
				}
				lo, hi := LayoutCount(sh.Layout)
				if n < lo || n > hi {
					out = append(out, errI("layout_members", sp+"/members", "%s shows %s; this one has %d (clip %s at %s)", LayoutLabel(sh.Layout), countRange(lo, hi), n, c.ID, Clock(sh.At)))
				}
				if sh.Layout == LayoutSideBySide && s.Output.Height > s.Output.Width {
					out = append(out, warnI("layout_shape", sp+"/layout", "side by side is narrow on a vertical video; stacked fits better (clip %s at %s)", c.ID, Clock(sh.At)))
				}
				if sh.Layout == LayoutStacked && s.Output.Width > s.Output.Height {
					out = append(out, warnI("layout_shape", sp+"/layout", "stacked is short and wide on a landscape video; side by side fits better (clip %s at %s)", c.ID, Clock(sh.At)))
				}
			case "card":
				for _, id := range sh.Members {
					if _, ok := pin.Member(id); !ok {
						out = append(out, errI("conversation_member", sp+"/members", "the card at %s is over %q, who isn't in the conversation", Clock(sh.At), id))
					}
				}
			case "broll":
				a, ok := facts.Asset(sh.BRoll.AssetID)
				switch {
				case !ok:
					out = append(out, errI("missing_asset", sp+"/broll/assetId", "the cutaway at %s is footage that isn't in this workspace (%s)", Clock(sh.At), sh.BRoll.AssetID))
				case !a.HasVideo:
					out = append(out, errI("not_video", sp+"/broll/assetId", "%s has no picture, so it can't be a cutaway", a.Name))
				case !a.InProject:
					out = append(out, errI("asset_not_in_project", sp+"/broll/assetId", "%s is not part of this project; add it to the project first", a.Name))
				default:
					end := c.Out
					if j+1 < len(c.Shots) {
						end = math.Min(end, c.Shots[j+1].At)
					}
					if a.Duration > 0 && sh.BRoll.In+(end-math.Max(sh.At, c.In)) > a.Duration+frameTol {
						out = append(out, warnI("broll_short", sp+"/broll", "%s ends before the shot does (it holds its last picture)", a.Name))
					}
				}
			}
			if j+1 < len(c.Shots) && fps > 0 {
				d := c.Shots[j+1].At - math.Max(sh.At, c.In)
				if d > 0 && d < 0.5 && sh.At >= c.In && c.Shots[j+1].At <= c.Out {
					out = append(out, warnI("shot_short", sp, "the shot at %s in clip %s lasts %.2f s, which reads as a flash; make it at least half a second", Clock(sh.At), c.ID, d))
				}
			}
		}
	}
	_ = clips
	return out
}

// notRecording warns when a member's picture isn't there for part of a shot
// (the recording starts later or ends earlier than the shot).
func notRecording(m ConversationMember, sh Shot, c Clip, facts Facts, path string) []Issue {
	a, ok := facts.Asset(m.AssetID)
	if !ok || a.Duration <= 0 {
		return nil
	}
	from := math.Max(sh.At, c.In)
	if m.SourceAt(from) < -0.05 {
		return []Issue{warnI("member_not_recording", path, "%s's recording starts at %s on the conversation's clock, after this shot does (%s); the picture is empty until then", m.Name, Clock(m.Start), Clock(from))}
	}
	if m.ClockAt(a.Duration) < from {
		return []Issue{warnI("member_not_recording", path, "%s's recording ends at %s, before this shot (%s)", m.Name, Clock(m.ClockAt(a.Duration)), Clock(from))}
	}
	return nil
}

func countRange(lo, hi int) string {
	if lo == hi {
		return fmt.Sprintf("%d people", lo)
	}
	return fmt.Sprintf("%d to %d people", lo, hi)
}
