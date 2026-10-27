package aiwork

// The adapters MetaRush ships (D224): each registers itself for the kinds of
// work its provider's key covers, and those of models that run on this
// computer (D278).
import (
	_ "github.com/metavert-io/metarush/internal/aiservices/did"
	_ "github.com/metavert-io/metarush/internal/aiservices/elevenlabs"
	_ "github.com/metavert-io/metarush/internal/aiservices/google"
	_ "github.com/metavert-io/metarush/internal/aiservices/heygen"
	_ "github.com/metavert-io/metarush/internal/aiservices/local"
	_ "github.com/metavert-io/metarush/internal/aiservices/syncso"
)
