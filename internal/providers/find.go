package providers

import "context"

// FindVoice finds a voice of the account by its ID or name (case-insensitive;
// a part of a name when only one voice has it), the way the narration's
// settings choose one.
func FindVoice(ctx context.Context, c *ElevenClient, ref string) (*Voice, error) {
	return findVoice(ctx, c, ref, "")
}
