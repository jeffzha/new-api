from pathlib import Path
import json
import fitz
from PIL import Image, ImageOps, ImageDraw

root=Path(r'E:\01-我的学习\study-notes\90-工作\01-九路科技\01-token平台\问题解决记录')
pdf=root/'NEXIGHT平台与WorkBuddy接入使用手册_V1.0.pdf'
dest=Path('tmp/pdfs/workbuddy-qa')
dest.mkdir(parents=True,exist_ok=True)
doc=fitz.open(pdf)
issues=[]
previews=[]
for i,page in enumerate(doc):
    pix=page.get_pixmap(matrix=fitz.Matrix(1.5,1.5),alpha=False)
    path=dest/f'page-{i+1:02}.png';pix.save(path)
    for word in page.get_text('words'):
        x0,y0,x1,y1,value,*_=word
        if x0<0 or y0<0 or x1>page.rect.width+1 or y1>page.rect.height+1:
            issues.append({'page':i+1,'out_of_bounds':value})
    im=Image.open(path).convert('RGB');im.thumbnail((298,422))
    cell=Image.new('RGB',(318,450),'#DAE3ED');cell.paste(im,((318-im.width)//2,8))
    ImageDraw.Draw(cell).text((14,430),f'PAGE {i+1}',fill='black')
    previews.append(cell)
sheet=Image.new('RGB',(318*4,450*2),'white')
for i,im in enumerate(previews):sheet.paste(im,((i%4)*318,(i//4)*450))
sheet.save(dest/'contact-sheet.png')
text='\n'.join(p.get_text() for p in doc)
assert len(doc)==8
assert 'claude-opus-5' in text and 'WorkBuddy' in text
assert '\ufffd' not in text
assert len(doc.get_toc())==8
assert len(doc[0].get_links())==7
assert not issues,issues
print(json.dumps({'pages':len(doc),'bookmarks':len(doc.get_toc()),'toc_links':len(doc[0].get_links()),'image_count':sum(len(p.get_images()) for p in doc),'text_chars':len(text),'issues':issues,'render_dir':str(dest)},ensure_ascii=False))
