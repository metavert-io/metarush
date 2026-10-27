package logbook

// Plain phrases for built-in connections (D303–D307): timelines from other
// editors, publishing to video platforms, Dropbox folders.
func init() {
	for k, v := range map[string]string{
		"timeline.read":     "read a timeline from another editing app",
		"timeline.import":   "make a cut in {p} from a timeline from another editing app",
		"publish.platforms": "look at the video platforms and the sign-ins to them", "publish.setup": "set up a video platform on this computer",
		"publish.connect": "sign in to a video platform", "publish.plan": "look at what posting a version of {p} would send",
		"publish.send":   "post version {r} of {p} to a video platform",
		"add.cloud.list": "list a Google Drive or Dropbox folder", "add.cloud": "bring files in from a Google Drive or Dropbox folder",
	} {
		opBase[k] = v
	}
}
