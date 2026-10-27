---
cut: 1
title: The garage episode
project: prj_01jabcdefghjkmnpqrstvwxyz0
base: rev_01jabcdefghjkmnpqrstvwxyz0
output:
  shape: landscape   # 1920 × 1080
  frame rate: 30
  platform: generic
  framing: faces   # crops keep the main face in frame (clips with a focus of their own keep it)
captions:
  on: true
  style: clean
  position: lower
  words per line: 6
  speaker names: true
  timing: transcript
mix:
  even out voices: on
  blend at cuts: 0.1 s
footage:
  interview-a:   ast_01jabcdefghjkmnpqrstvwxyz0 sha256:9f2c4e1a7b3d5f60   # interview-a.mov · 0:27.3
  interview-a-2: ast_01jabcdefghjkmnpqrstvwxyz3 sha256:aaaa222233334444   # Interview A.mov · 1:00
---

## Conversations

- call: cnv_01jabcdefghjkmnpqrstvwxyz0 · quieter when not speaking by 18 dB
  Member: ada "Ada" · interview-a · starts at 0:00 · tag "Host" · focus on faces
  Member: sam "Sam" · interview-a-2 · starts at 0:00.412 · drift +35.5 ppm · crop bottom 8% · focus on faces · look exposure 106%, warmth -3%

## Clips

1. talk: conversation call 0:00 → 0:13.5
   > Ada: “When I started building my …” … “… we built it very quickly.”

   Shot: 0:00 side by side · why "Open on both of them."
   Shot: 0:00.9 ada · why "Ada starts the story at 0:00.5; the picture follows 0.4 s later (J-cut)."
   Shot: 0:06.4 sam · dissolve 0.3 s
   Shot: 0:10.2 side by side ada, sam · why "Both laugh."

   Why: The question and the first answer.

↳ cut · sound changes in a pause

2. answer: conversation call 0:15.8 → 0:19.5 · without sound from ada
   > (no words spoken here)

   Shot: 0:15.8 card "What would you build?" over sam
