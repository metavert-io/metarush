# Fixture images

Small still images that `scripts/make-fixtures.sh` turns into `visual-a.mp4` (cuts, a slow pan, a person walking in,
a talking head, on-screen text and a dog) and `visual-b.mp4` (the same two people in other photos: one walking across
the room, one at the edge of the picture above a sign, one low in the picture, and someone else) for the
visual-change detector, the on-screen index, recognizing faces across clips, framing that follows movement and the
on-screen checks of renders. Each file was downscaled and re-encoded from the Wikimedia Commons original below. All
of them are public domain or CC0, so they can be copied, changed and redistributed without asking.

| File | Original on Wikimedia Commons | License |
|---|---|---|
| `portrait.jpg` | [Astronaut Christina M. Hammock Koch (cropped).jpg](https://commons.wikimedia.org/wiki/File:Astronaut_Christina_M._Hammock_Koch_(cropped).jpg) | Public domain (U.S. government work) |
| `person.jpg` | [Victor Glover in a NASA news conference at the Kennedy Space Center in Florida, U.S. on August 8, 2023.jpg](https://commons.wikimedia.org/wiki/File:Victor_Glover_in_a_NASA_news_conference_at_the_Kennedy_Space_Center_in_Florida,_U.S._on_August_8,_2023.jpg) | Public domain (U.S. government work) |
| `dog.jpg` | [Queensland heeler - Public Domain.jpg](https://commons.wikimedia.org/wiki/File:Queensland_heeler_-_Public_Domain.jpg) | CC0 |
| `beach.jpg` | [Unawatuna Beach panorama 2014.jpg](https://commons.wikimedia.org/wiki/File:Unawatuna_Beach_panorama_2014.jpg) | CC0 |
| `portrait-b.jpg` | [NASA Candidate Christina M Hammock.jpg](https://commons.wikimedia.org/wiki/File:NASA_Candidate_Christina_M_Hammock.jpg) (the person of `portrait.jpg`, another photo) | Public domain (U.S. government work) |
| `person-b.jpg` | [Victor J. Glover in 2018.jpg](https://commons.wikimedia.org/wiki/File:Victor_J._Glover_in_2018.jpg) (the person of `person.jpg`, another photo) | Public domain (U.S. government work) |
| `portrait-c.jpg` | [Jessica Meir (portrait crop).jpg](https://commons.wikimedia.org/wiki/File:Jessica_Meir_(portrait_crop).jpg) | Public domain (U.S. government work) |

The people in the portraits are only test subjects: the fixtures name nobody, and the tests check what is detected
(a face, its size and place) and whether two faces are the same person (a name the test makes up is confirmed on one
and recognized on the other), never who anyone is.
