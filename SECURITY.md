# Security policy

## Reporting a vulnerability

Please report security problems privately, through GitHub: open this
repository's **Security** tab and choose **Report a vulnerability**. That
creates a private advisory that only the maintainers can see. Please do not
open a public issue, pull request or discussion about a vulnerability.

A useful report says what you found, how to reproduce it, which version or
commit you tested, and what an attacker could do with it. Fixes land on
`main` and ship in the next release; older releases are not patched
separately.

## How MetaRush protects your work

- [docs/security.md](docs/security.md): the dashboard's sign-in, remote access
  over Tailscale, the render jail and what each surface may do.
- [docs/what-may-leave.md](docs/what-may-leave.md): per-client rules for what
  may leave your computer, and the receipt that proves what did.
- [docs/abilities.md](docs/abilities.md): how added abilities run in the
  operating system's sandbox, reaching only the hosts they declare.
- README, "Your rules": consent for faces and voices, budgets, and the ship
  gate.
