package tools

// LinuxbrewPrefix is where Homebrew on Linux lives. It is the one system
// folder under /home that MetaRush names, so it is written only here (the
// publish check's home-folder rule leaves this file out:
// scripts/check-public.skip).
const LinuxbrewPrefix = "/home/linuxbrew/.linuxbrew"
