package logbook

// Plain phrases for What may leave (D288–D291) and ability files (D292).
func init() {
	for k, v := range map[string]string{
		"content.leave": "check what of {p} may leave this computer", "content.leave.set": "change what of a client may leave this computer",
		"project.leaves": "read what of {p} left this computer",
		"ability.export": "export an ability to a file", "ability.import": "bring in an ability from a file",
	} {
		opBase[k] = v
	}
}
