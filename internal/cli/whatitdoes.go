package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/breakdown"
	"github.com/metavert-io/metarush/internal/ops"
)

// metarush footage what-it-does <clip> (D399): what a video does, from what
// MetaRush found in it — its sections with times, pacing, voices, captions,
// words on screen, what plays under the voices and how it ends; the numbers
// with --details, the card with --json. metarush footage reference <clip>
// [--mine] (D398): mark a clip as a reference (someone else's video, kept to
// learn from, that no cut plays), or say it's yours (people only).

func footageWhatItDoesCmd() *cobra.Command {
	var in breakdown.WhatItDoesIn
	return &cobra.Command{
		Use:   "what-it-does <clip>",
		Short: "Say what a video does: its sections with times (hook, intro, parts, call to action, end card), pacing, who speaks, captions, words on screen, music and how it ends",
		Long: `Read what a video does from what MetaRush found in it: its transcript and voices, its cuts, pauses and
loudness, and (on a Mac) what's on screen. Nothing is sent anywhere: it is worked out on this computer.

Its sections are found from the cuts, pauses, sentences and words on screen; the card says what happens in each in
MetaRush's own words. --details adds the numbers (times, shot lengths, words a minute, levels), --json the card.`,
		Example: "  metarush footage what-it-does ast_…\n  metarush footage what-it-does \"Fake video\" --details",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			return runOp(c, "footage.whatItDoes", in, func(o any) { printWhatItDoes(o.(*breakdown.WhatItDoesOut), detailsMode(c)) })
		},
	}
}

func printWhatItDoes(o *breakdown.WhatItDoesOut, details bool) {
	fmt.Println(theirs(o.Clip.Title) + clipWhere(o.Clip))
	for _, l := range o.Card.Lines {
		fmt.Println("  " + l)
	}
	if details {
		c := o.Card
		fmt.Printf("    clip %s · %.1f s · %dx%d · ready %v\n", o.Clip.ID, c.Length, c.Width, c.Height, c.Ready)
		for _, s := range c.Sections {
			fmt.Printf("    %-15s %6.2f–%6.2f  %5.2f s  shots %d  words %d  speech %.0f%%  question %v  text %v  faces %d %s  (%s)\n",
				s.Name, s.From, s.To, s.Length, s.Shots, s.Words, s.Speech*100, s.Question, s.Text, s.Faces, s.Shot, s.Found)
		}
		p := c.Pacing
		fmt.Printf("    pacing: %d shots, average %.1f s, median %.1f s, %.1f–%.1f s; %d words, %.0f a minute, talking %.0f%%, %d pauses (longest %.1f s)\n",
			p.Shots, p.ShotSeconds, p.MedianShot, p.Shortest, p.Longest, p.Words, p.WordsPerMinute, p.Talking*100, p.Pauses, p.LongestPause)
		for _, sp := range c.Speakers {
			fmt.Printf("    voice %s %s: %.1f s (%.0f%%)\n", sp.Label, sp.Name, sp.Seconds, sp.Share*100)
		}
		cp := c.Captions
		fmt.Printf("    captions: looked %v, burned %v, covers %.0f%%, %.1f words at a time, %s, capitals %v, %s, look %s\n",
			cp.Looked, cp.Burned, cp.Covers*100, cp.WordsAtATime, cp.Position, cp.Capitals, cp.Size, cp.Look)
		for _, g := range c.Graphics {
			fmt.Printf("    on screen: %s %.1f–%.1f %s %s %q\n", g.Kind, g.From, g.To, g.Where, g.Size, g.Text)
		}
		s := c.Sound
		if s.Speech != nil && s.Under != nil {
			fmt.Printf("    sound: voices %.1f dB, pauses %.1f dB (%.1f under), bed %s, throughout %v\n", *s.Speech, *s.Under, s.Below, s.Bed, s.Throughout)
		}
		if o.Clip.Link != "" {
			fmt.Printf("    from %s (%s %s)\n", o.Clip.Link, o.Clip.Platform, o.Clip.Who)
		}
	}
}

// clipWhere says a clip is a reference, and where it came from.
func clipWhere(c breakdown.WhatClip) string {
	var parts []string
	if c.Reference {
		parts = append(parts, "a reference: someone else's video, kept to learn from")
	}
	if c.Who != "" {
		parts = append(parts, "posted by "+c.Who)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func footageReferenceCmd() *cobra.Command {
	var in ops.FootageReferenceIn
	var mine bool
	c := &cobra.Command{
		Use:   "reference <clip> [--mine]",
		Short: "Mark a clip as a reference (someone else's video, kept to learn from, that no cut plays), or say it's your own video",
		Long: `A reference is someone else's video kept to learn from: what it does and how it is paced. No cut plays it, and
Recipes never take it as footage. "Start from a video you like" brings a link in as one.

--mine says the video is yours, so cuts can play it again. Only a person says so: in the dashboard (the clip's
This is mine…) or in their own terminal.`,
		Example: "  metarush footage reference ast_…\n  metarush footage reference ast_… --mine",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			print := func(o any) { fmt.Println(o.(*ops.FootageReferenceOut).Message) }
			if !mine {
				return runOp(c, "footage.reference", in, print)
			}
			if agentAtTheKeys(c) || !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person says a video is theirs, in the dashboard or in their own terminal.",
					"Open the clip in the dashboard and choose This is mine…, or run this command yourself.")
			}
			return peopleOnly(c, "footage.reference.mine", in, "say a video is theirs", print)
		},
	}
	c.Flags().BoolVar(&mine, "mine", false, "say the video is yours, so cuts can play it (people only)")
	return c
}
