# Stock videos and photos from Pexels

Searches Pexels for a few words and brings in up to six videos (an MP4 up to 1920 wide, the closest to Full HD) or
photos (about 1,900 px wide). Each file keeps its licence, its page on Pexels and the credit line Pexels asks for
("Video by … on Pexels"): a clip records them as its rights, a photo on its source, and a delivery lists them with
its credits.

- **Licence of the files:** the Pexels License — free to use and change, credit not required but asked for. Not
  allowed: selling unchanged copies, showing identifiable people in a bad light, implying they endorse you. Pexels'
  API guidelines ask that you show where the files came from: keep the credit line in the description.
- **Licence of this ability's code:** MIT.
- **Cost:** free. Pexels allows 200 requests an hour and 20,000 a month per key; one run makes one search and one
  request per file.
- **Key:** `pexels`, free with a Pexels account (pexels.com/api). An owner adds it on each computer:
  `metarush ability setup pexels-stock --key pexels`. The code never sees it: MetaRush puts it in the
  Authorization header, only on requests to api.pexels.com.
- **What leaves this computer:** the words you search for, to api.pexels.com. Files come from images.pexels.com and
  videos.pexels.com without the key. A video Pexels offers only from another host is left out.
- **Who starts a run:** a person, each time (it uses a key).

Good searches are short and concrete: "ocean waves at dusk", "city traffic night", "hands typing laptop". Use
`kind: photos` for stills and `orientation: portrait` for shorts.
