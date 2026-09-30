"""Preserve the scanned May 2026 V1 Module 2 questions as private JSON assets.

Run after extracting page-NNN.png with pypdf and producing ocr.json with
ocr-scans.ps1. OCR provides descriptions; displayed content comes from the scans,
so mathematical notation, underlines, tables and figures are not reconstructed.
"""
import base64
import argparse
import io
import json
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[1] / "private" / "may-2026"
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument("--prepare",metavar="PDF",help="extract original scans and OCR-sized copies before running ocr-scans.ps1")
args=parser.parse_args()
if args.prepare:
    from pypdf import PdfReader
    ROOT.mkdir(parents=True,exist_ok=True)
    pdf=PdfReader(args.prepare)
    if len(pdf.pages)!=103: raise ValueError("Expected the 103-page May 2026 V1 source")
    for n in [*range(28,56),*range(78,100),101,103]:
        images=list(pdf.pages[n-1].images)
        if len(images)!=1: raise ValueError(f"Page {n} is not a single scan")
        im=Image.open(io.BytesIO(images[0].data)).convert("RGB")
        im.save(ROOT/f"page-{n:03}.png")
        im.thumbnail((2600,2600));im.save(ROOT/f"ocr-{n:03}.png")
    print("Prepared scans. Run ocr-scans.ps1, then run this script without --prepare.")
    raise SystemExit(0)
OCR = {int(p["file"][4:7]): p for p in json.loads((ROOT / "ocr.json").read_text(encoding="utf-8-sig"))}
ASSETS = []
QUESTIONS = []
PREVIEWS = []
MANIFEST=json.loads((ROOT/"source-manifest.json").read_text(encoding="utf-8"))
RW_KEYS=MANIFEST["readingKeys"]
MATH_KEYS=MANIFEST["mathKeys"]

def words(n):
    return [w for line in OCR[n]["lines"] for w in line["words"]]

def description(n, box):
    x0,y0,x1,y1 = box
    rows = []
    for line in OCR[n]["lines"]:
        selected = [w for w in line["words"] if x0 <= w["x"] < x1 and y0 <= w["y"] < y1]
        if selected:
            rows.append((min(w["y"] for w in selected), " ".join(w["text"] for w in sorted(selected,key=lambda w:w["x"]))))
    return "\n".join(t for _,t in sorted(rows))

def scan(n):
    im = Image.open(ROOT/f"page-{n:03}.png").convert("RGB")
    # Match OCR coordinates, while retaining a sharp original scan.
    im.thumbnail((2600,2600))
    return im

def crop(im, box):
    im = im.crop(tuple(map(int,box)))
    a = np.array(im.convert("L"))
    ys,xs = np.where(a < 140)  # Ignore faint watermarks when locating content.
    if len(xs):
        im = im.crop((max(0,int(xs.min())-12),max(0,int(ys.min())-12),min(im.width,int(xs.max())+13),min(im.height,int(ys.max())+13)))
    return im

def image_block(im, asset_id, alt):
    stream=io.BytesIO();im.save(stream,format="PNG",optimize=True)
    ASSETS.append({"id":asset_id,"mimeType":"image/png","dataBase64":base64.b64encode(stream.getvalue()).decode()})
    im.save(ROOT/f"{asset_id}.png")
    return {"kind":"image","url":"asset:"+asset_id,"alt":alt or "Original scanned question content.","width":im.width,"height":im.height,"showAltAsCaption":False,"zoomable":asset_id.endswith(("-passage","-graph","-paragraph","-prompt"))}

def reading(n,position):
    im=scan(n);w,h=im.size
    labels={letter:next(v for v in words(n) if v["text"]==letter and 1450<v["x"]<1700) for letter in "ABCD"}
    left=min(v["x"] for v in labels.values());split=left-80
    qid=f"may2026-m2-rw-{position}"
    passage_box=(15,0,split,h)
    if position==13:
        # Page 40 cuts off the paragraph; page 41 supplies its ending, but
        # cuts off the chart title and question stem. Combine the complete regions.
        p40=scan(40);p41=scan(41)
        passage=[image_block(crop(p40,(15,0,1410,1200)),qid+"-graph", "Percentages of Form 4720s filed and total penalties assessed on private foundations, by taxable activity, 2005. Original bar chart and legend."),image_block(crop(p41,(15,1190,1440,p41.height)),qid+"-paragraph",description(41,(15,1190,1440,p41.height)))]
    else:
        passage=[image_block(crop(im,passage_box),qid+"-passage",description(n,passage_box))]
    prompt_box=(left+55,0,w-100,labels["A"]["y"]-25)
    prompt=description(n,prompt_box)
    # Exclude the small ABC tool icon and original question-number badge.
    prompt=prompt.replace("ABC","").strip()
    if position==7: prompt="Which choice best describes the function of the underlined portion in the text as a whole?"
    choices=[]
    for i,letter in enumerate("ABCD"):
        y0=labels[letter]["y"]-10
        y1=labels["ABCD"[i+1]]["y"]-20 if i<3 else h
        box=(left+42,y0,w-15,y1)
        choices.append({"id":letter,"content":[image_block(crop(im,box),qid+"-"+letter,description(n,box))]})
    public={"id":qid,"sectionId":"reading-writing","position":position,"kind":"multiple-choice","passages":[{"id":"passage","content":passage}],"prompt":[{"kind":"text","text":prompt}],"choices":choices}
    original=RW_KEYS[position-1]
    # Preserve the printed key in private provenance while correcting clear errors.
    answer,note=MANIFEST["readingCorrections"].get(str(position),(original,""))
    QUESTIONS.append({"public":public,"correctAnswer":{"kind":"choice","choiceId":answer},"difficulty":"hard","explanation":[],"source":{"pdf":"SAT May 2026 V1 Full.pdf","module":2,"pages":[40,41] if position==13 else [n],"originalAnswer":original,"keyCorrection":note,"representation":"scan crops with OCR descriptions"}})
    PREVIEWS.append((qid,passage[0],public["prompt"],choices))

def math(n,position):
    im=scan(n);w,h=im.size;qid=f"may2026-m2-math-{position}";answer=MATH_KEYS[position-1]
    numeric=answer not in "ABCD"
    if numeric:
        answer_y=next(v["y"] for v in words(n) if v["text"]=="Answer")
        box=(155,218,w-150,answer_y-130)
        prompt=[image_block(crop(im,box),qid+"-prompt",description(n,box))]
        public={"id":qid,"sectionId":"math","position":position,"kind":"numeric","prompt":prompt,"studentProducedResponseDirections":True}
        key={"kind":"numeric","value":answer}
        if str(position) in MANIFEST["numericAccepted"]: key["accepted"]=MANIFEST["numericAccepted"][str(position)]
        choices=[]
    elif position==MANIFEST["mathTableQuestion"]["position"]:
        table=MANIFEST["mathTableQuestion"]
        public={"id":qid,"sectionId":"math","position":position,"kind":"multiple-choice","prompt":[{"kind":"text","text":table["prompt"]}],"choices":[]}
        for letter,freq in zip("ABCD",table["frequencies"]):
            public["choices"].append({"id":letter,"content":[{"kind":"table","caption":"Frequency table","headers":table["headers"],"rows":[[str(v),str(f)] for v,f in zip(table["values"],freq)]}]})
        prompt=public["prompt"];choices=public["choices"];key={"kind":"choice","choiceId":answer}
    else:
        counts=(np.array(im.convert("L"))[:,200:min(1550,w-50)]<110).sum(axis=1)
        groups=[]
        for y in np.where(counts>750)[0]:
            if not groups or y>groups[-1][-1]+1:groups.append([int(y)])
            else:groups[-1].append(int(y))
        edges=groups[-8:]
        assert len(edges)==8,(n,edges)
        box=(265,115,w-280,edges[0][0]-15)
        prompt=[image_block(crop(im,box),qid+"-prompt",description(n,box))]
        choices=[]
        for i,letter in enumerate("ABCD"):
            box=(345,edges[2*i][-1]+7,w-300,edges[2*i+1][0]-6)
            choices.append({"id":letter,"content":[image_block(crop(im,box),qid+"-"+letter,description(n,box))]})
        public={"id":qid,"sectionId":"math","position":position,"kind":"multiple-choice","prompt":prompt,"choices":choices}
        key={"kind":"choice","choiceId":answer}
    QUESTIONS.append({"public":public,"correctAnswer":key,"difficulty":"hard","explanation":[],"source":{"pdf":"SAT May 2026 V1 Full.pdf","module":2,"pages":[n],"originalAnswer":answer,"representation":"scan crops; table in question 18 transcribed exactly"}})
    PREVIEWS.append((qid,prompt[0],[],choices))

for position,n in enumerate([*range(28,40),40,*range(42,56)],1): reading(n,position)
for position,n in enumerate(range(78,100),1): math(n,position)
assert len(QUESTIONS)==49
bank={"schemaVersion":2,"title":"May 2026 V1 - Reading and Writing Module 2 and Math Module 2","questions":QUESTIONS,"assets":ASSETS}
(ROOT/"module2-bank.json").write_text(json.dumps(bank,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
print(f"Created {len(QUESTIONS)} questions and {len(ASSETS)} PNG assets; JSON size {(ROOT/'module2-bank.json').stat().st_size:,} bytes")
