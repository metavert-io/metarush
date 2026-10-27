package logbook

// Plain phrases for accounts and sign-in (D183) and Preferences (D208).
func init() {
	for k, v := range map[string]string{
		"auth.status": "read how people sign in", "auth.method.set": "change how people sign in", "auth.reset": "put sign-in back to just the owner",
		"users.list": "read the list of people", "users.invite": "invite a person", "users.resetLink": "make a reset-password link",
		"users.revoke": "take back an invite or reset link", "users.disable": "turn a person's account off", "users.enable": "turn a person's account back on",
		"users.remove": "remove a person's account", "users.role": "change a person's role",
		"account.password": "change their password", "account.update": "change their name or email", "account.signoutEverywhere": "sign out everywhere", "account.avatar": "set or remove their picture",
		"account.preferences": "read their preferences", "account.preferences.set": "change their preferences",
	} {
		opBase[k] = v
	}
	for k, v := range map[string]string{
		"auth.signin": "signed in", "auth.invite.used": "accepted an invite and chose a password", "auth.reset.used": "chose a new password from a reset link",
		"auth.method": "changed how people sign in", "auth.reset": "put sign-in back to just the owner", "auth.locked": "locked sign-in for an email after wrong passwords",
		"users.invite": "invited a person", "users.resetLink": "made a reset-password link", "users.revoke": "took back a link",
		"users.state": "changed whether a person can sign in", "users.role": "changed a person's role",
		"account.password": "changed their password", "account.update": "changed their name or email", "account.signoutEverywhere": "signed out everywhere", "account.avatar": "changed their picture",
		"account.preferences": "changed whether costs are estimated before their edits", "workspace.estimates": "changed whether everyone sees estimates first",
	} {
		eventPhrases[k] = v
	}
}
