package logbook

// Plain phrases for review links for people outside the workspace (D313–D317).
func init() {
	for k, v := range map[string]string{
		"share.plan": "check what a review link to {p} would show", "share.create": "send version {r} of {p} for review outside the workspace",
		"share.list": "list the review links of {p}", "share.link": "copy a review link again", "share.revoke": "take back a review link to {p}",
		"share.sync": "bring in comments from review links", "share.settings": "check who may send review links",
		"share.settings.set": "change who may send review links",
	} {
		opBase[k] = v
	}
}
