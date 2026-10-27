# Where the reference texts come from

The texts of the readings `scripts/fetch-speech.sh` downloads (the recordings themselves are never committed; the
script checks each against the SHA-1 its publisher gives). Each file is the text as read, cleaned to what is read:
titles, verse numbers and LibriVox's spoken preamble and outro left out. All are in the public domain.

| File | Language | The text | Where it came from | Status | The recording read from it |
|---|---|---|---|---|---|
| `es-tito.txt` | Spanish | Titus 1–3, Reina-Valera 1909 | Spanish Wikisource, *Biblia Reina-Valera 1909/Tito* | public domain | LibriVox `tito_1602_librivox` (Public Domain Mark 1.0) |
| `fr-ruth.txt` | French | Ruth 1–4, Louis Segond 1910 | French Wikisource, *Bible Segond 1910/Livre de Ruth* | public domain | LibriVox `ruth_lsg_librivox` (public domain) |
| `de-galerie.txt` | German | Franz Kafka, *Auf der Galerie* | German Wikisource | public domain | LibriVox `kafka_galerie_librivox` (public domain) |
| `de-laerm.txt` | German | Franz Kafka, *Großer Lärm* | German Wikisource | public domain | LibriVox `kafka_laerm_librivox` (public domain) |
| `de-haewelmann.txt` | German | Theodor Storm, *Der kleine Häwelmann* | German Wikisource | public domain | LibriVox `haewelmann_librivox` (public domain) |
| `pt-apologo.txt` | Portuguese | Machado de Assis, *Um apólogo* | Portuguese Wikisource | public domain | LibriVox `contos_brasileiros_0807_librivox`, section 14 (public domain) |
| `pt-filha.txt` | Portuguese | Artur Azevedo, *A filha do patrão* | Portuguese Wikisource | public domain | the same LibriVox collection, section 1 (public domain) |
| `ja-ningyo1.txt`, `ja-ningyo2.txt` | Japanese | Ogawa Mimei, *赤いろうそくと人魚* (parts 1 and 2) | Aozora Bunko, card 51005 (新字新仮名), ruby removed | public domain | LibriVox `akai_rosoku_um_librivox`, sections 1–2 (CC0 1.0) |

The English readings the script also fetches are only listened to for their language and have no text here.
