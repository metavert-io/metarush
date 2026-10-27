# voices.sh — pick the macOS `say` voices the fixture scripts speak with.
#
#   . scripts/lib/voices.sh
#   VOICE="$(pick_voice Samantha "Flo (English (US))" Kathy)"   # the first one installed, else "" (the system's)
#
# `say -v '?'` lists a voice by its bare name on older macOS ("Samantha") and, on
# newer ones, with its language for many voices ("Samantha (English (US))"). A
# bare candidate matches either way and is returned bare (`say -v Samantha`
# takes it), so every Mac picks the same voice and writes the same ground truth.
# A candidate with its language in it ("Flo (English (US))") matches only itself.

# say_voices lists the installed voices' names, one a line.
say_voices() {
  say -v '?' | sed -E 's/[[:space:]]+[a-z]{2,3}_[A-Za-z0-9]{2,4}[[:space:]]+#.*$//'
}

# pick_voice <candidate>... prints the first candidate installed, else nothing.
pick_voice() {
  local v list
  list="$(say_voices)"
  for v in "$@"; do
    if printf '%s\n' "$list" | grep -Fxq -- "$v"; then printf '%s' "$v"; return; fi
    case "$v" in
      *"("*) ;;
      *) if printf '%s\n' "$list" | grep -q "^$v (English ("; then printf '%s' "$v"; return; fi ;;
    esac
  done
  printf ''
}
