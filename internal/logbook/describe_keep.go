package logbook

// Plain phrases for what a project keeps (D166): kept things, sign-offs,
// ideas for MetaRush and conversation attachments.
func init() {
	for k, v := range map[string]string{
		"keep.add": "keep something from a conversation", "keep.list": "read what conversations offered to keep in {p}",
		"keep.decline":      "decline something a conversation offered to keep",
		"cut.signoffs.list": "read the sign-offs of {p}", "cut.signoffs.add": "add a sign-off to {p}", "cut.signoffs.done": "tick a sign-off of {p}",
		"cut.signoffs.undo": "untick a sign-off of {p}", "cut.signoffs.remove": "take a sign-off off {p}",
		"improve.add":      "add an idea to MetaRush's improvement list",
		"assistant.attach": "attach a file to a conversation",
	} {
		opBase[k] = v
	}
}
