package logbook

// Plain phrases for abilities (D230–D235): capabilities added without a
// release, checked, approved by owners, set up per computer and run in the
// sandbox.
func init() {
	for k, v := range map[string]string{
		"ability.list": "list the abilities", "ability.find": "look for an ability", "ability.show": "look at an ability",
		"ability.check": "check an ability", "ability.approve": "approve an ability", "ability.revoke": "take back an ability's approval",
		"ability.setup": "set up an ability on this computer", "ability.plan": "plan a run of an ability", "ability.run": "start a run of an ability",
		"ability.runs": "look at runs of abilities",
		// Community catalogues (D481–D486).
		"ability.catalogues": "look at the catalogues of abilities this workspace trusts", "ability.catalogues.add": "trust a catalogue of abilities",
		"ability.catalogues.remove": "stop trusting a catalogue of abilities", "ability.browse": "browse the catalogues of abilities",
		"ability.catalogues.import": "bring in an ability from a catalogue",
	} {
		opBase[k] = v
	}
}
