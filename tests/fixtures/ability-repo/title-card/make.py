import json, os

inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
words = inputs["words"].replace("<", "").replace("&", "and")
svg = ('<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720"><rect width="1280" height="720" fill="#264653"/>'
       '<text x="640" y="380" font-size="72" text-anchor="middle" fill="#f4f1de">' + words + '</text></svg>')
out = os.environ["METARUSH_OUTPUT"]
open(os.path.join(out, "card.svg"), "w").write(svg)
json.dump({"files": [{"path": "card.svg", "title": "Card: " + words, "licence": "CC0"}]}, open(os.path.join(out, "result.json"), "w"))
print("made a card for", words)
