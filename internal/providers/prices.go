package providers

import (
	"fmt"

	"github.com/metavert-io/metarush/internal/costs"
)

// ElevenLabsPriceTable is what MetaRush counts ElevenLabs' work at to say in
// dollars what the AI voice and a clearer voice in the cloud cost (D208):
// ElevenLabs' own pay-as-you-go API prices, with the day they were read.
// Budgets and the plan's allowance still count credits; credits that come
// with a plan may cost less than these prices.
type ElevenLabsPriceTable struct {
	// Checked is the day the prices were read (YYYY-MM-DD) and Source where.
	Checked string
	Source  string
	// SpeechPerThousandCredits: text to speech is $0.08 per 1,000 characters
	// on Multilingual v2, v3 and v4 (a credit a character) and $0.04 on Flash
	// and Turbo (half a credit a character): $0.08 per 1,000 credits either way.
	SpeechPerThousandCredits float64
	// IsolationPerMinute: the Voice Isolator, $0.12 a minute of sound (1,000 credits).
	IsolationPerMinute float64
}

// ElevenLabsPrices are the prices in force. A price change is a change here
// (with the new date), like the generated-video price tables (D123).
var ElevenLabsPrices = ElevenLabsPriceTable{
	Checked:                  "2026-10-23",
	Source:                   "https://elevenlabs.io/pricing/api",
	SpeechPerThousandCredits: 0.08,
	IsolationPerMinute:       0.12,
}

// ElevenLabsTitle is the service's name as people read it.
const ElevenLabsTitle = "ElevenLabs"

// IsolationModel is the name of ElevenLabs' voice isolation, as its price list says it.
const IsolationModel = "Voice Isolator"

// IsolationCreditsPerMinute is what voice isolation counts against a plan.
const IsolationCreditsPerMinute = 1000

// SpeechUSD is what text to speech of so many credits costs, in dollars.
func SpeechUSD(credits float64) float64 {
	return credits / 1000 * ElevenLabsPrices.SpeechPerThousandCredits
}

// IsolationUSD is what voice isolation of so many credits costs (1,000 a minute).
func IsolationUSD(credits float64) float64 {
	return credits / IsolationCreditsPerMinute * ElevenLabsPrices.IsolationPerMinute
}

// SpeechRate says the speech price and its date, for an estimate's part.
func SpeechRate() string {
	return fmt.Sprintf("$%.2f per 1,000 credits (ElevenLabs' prices of %s)", ElevenLabsPrices.SpeechPerThousandCredits, costs.Day(ElevenLabsPrices.Checked))
}

// IsolationRate says the voice isolation price and its date.
func IsolationRate() string {
	return fmt.Sprintf("$%.2f a minute of sound, 1,000 credits (ElevenLabs' prices of %s)", ElevenLabsPrices.IsolationPerMinute, costs.Day(ElevenLabsPrices.Checked))
}

// PlanCreditsNote is what an ElevenLabs estimate says about plans.
const PlanCreditsNote = "Priced at ElevenLabs' pay-as-you-go rates; credits that come with your ElevenLabs plan may cost less. The project's budget counts credits."
