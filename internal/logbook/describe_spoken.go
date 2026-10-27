package logbook

// Plain phrases for words changed in a speaker's own voice copy (D308–D312).
func init() {
	for k, v := range map[string]string{
		"cut.say.estimate": "price new words in a speaker's own voice for {p}",
		"cut.say.make":     "ask for new words in a speaker's own voice for {p}",
		"cut.say.place":    "put new words in a speaker's own voice into {p}",
		"cut.say.undo":     "take changed words out of {p}",
		"cut.say.list":     "list the words changed in speakers' voices in {p}",
		"cut.say.show":     "look at words changed in a speaker's voice",
		"cut.say.mouth":    "ask for the mouth to match new words in {p}",
	} {
		opBase[k] = v
	}
	cutVerbChecked["cut.say.place"] = "checked putting new words in a speaker's own voice into {p}"
	cutVerbChecked["cut.say.undo"] = "checked taking changed words out of {p}"
	pastOf["undo"] = "undid"
}
