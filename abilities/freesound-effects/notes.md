# Sound effects from Freesound

Searches Freesound for a few words and brings in up to six sounds no longer than you say (30 seconds unless you
change it), as Freesound's high-quality previews (MP3, about 128 kbps). Each sound keeps its licence, its page, who
recorded it and the credit line its licence asks for, e.g. “Rain on a tin roof” by rain_maker, CC BY 4.0, via
Freesound; it records them as its rights, and a delivery lists them with its credits and in its `.notes.md` file.

- **Licences of the sounds:** CC0 (no credit needed) and CC BY (credit needed) by default, or CC0 only. Sounds under
  "non-commercial" (CC BY-NC) or Sampling+ are left out, and the run's log says so.
- **Freesound's API terms:** using the API is free **only for non-commercial purposes**; for commercial work
  Freesound asks you to contact them first. That is about the API, not the sounds' licences: check it before using
  this ability for a client.
- **Licence of this ability's code:** MIT.
- **Cost:** free. Freesound allows 60 requests a minute and 2,000 a day per key.
- **Key:** `freesound`, free with a Freesound account (freesound.org/apiv2/apply). An owner adds it on each computer:
  `metarush ability setup freesound-effects --key freesound`. The code never sees it: MetaRush puts it in the
  Authorization header (as `Token <key>`), only on requests to freesound.org.
- **What leaves this computer:** the words you search for, to freesound.org. The previews come from
  cdn.freesound.org without the key.
- **Who starts a run:** a person, each time (it uses a key).

What's missing: the original files (WAV, FLAC at full quality) need a Freesound sign-in through OAuth2, which
abilities can't do; MetaRush keeps keys, not sign-ins. Previews are enough for most effects under a voice.

Search with the thing and the action: "door creak", "rain on roof", "crowd cheering", "whoosh".
