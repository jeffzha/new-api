from pathlib import Path
from html import escape
import json
import shutil

from PIL import Image
from reportlab.pdfgen import canvas
from reportlab.lib.colors import HexColor, white
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import Paragraph
from reportlab.lib.styles import ParagraphStyle

ROOT = Path(r'E:\01-我的学习\study-notes\90-工作\01-九路科技\01-token平台\问题解决记录')
RES = ROOT / 'NEXIGHT-WorkBuddy手册资源'
RES.mkdir(parents=True, exist_ok=True)
IMAGES = RES / 'images'
IMAGES.mkdir(exist_ok=True)
OUT = ROOT / 'NEXIGHT平台与WorkBuddy接入使用手册_V1.0.pdf'
SOURCE = Path(r'C:\Users\30372\AppData\Roaming\Typora\typora-user-images')
ORIGINALS = RES / 'originals'
ORIGINALS.mkdir(exist_ok=True)
if (ORIGINALS / 'image-20260911175533851.png').exists():
    SOURCE = ORIGINALS
pdfmetrics.registerFont(TTFont('CN', r'C:\Windows\Fonts\Deng.ttf'))
pdfmetrics.registerFont(TTFont('CNB', r'C:\Windows\Fonts\Dengb.ttf'))
pdfmetrics.registerFontFamily('CN', normal='CN', bold='CNB')
W,H=595.276,841.89
LEFT,RIGHT=44,551.276
WIDTH=RIGHT-LEFT
INK='#142B45'; MUTED='#586B7D'; BLUE='#1768D5'; TEAL='#087E83'; LIGHT='#EFF5FC'; LINE='#D8E3ED'
c=canvas.Canvas(str(OUT),pagesize=(W,H))
c.setTitle('NEXIGHT 平台与 WorkBuddy 接入使用手册 | V1.0')
c.setAuthor('广州观枢智能科技有限公司')
c.setSubject('创建 API 密钥、选择模型、配置 WorkBuddy 与连接验证')
source_lines=['# NEXIGHT 平台与 WorkBuddy 接入使用手册','版本：V1.0 | 更新日期：2026-09-11','']
page_count=8

def text(x,y,s,size=10,color=INK,bold=False):
    c.setFillColor(HexColor(color));c.setFont('CNB' if bold else 'CN',size);c.drawString(x,H-y-size,s)

def para(x,y,s,width=WIDTH,size=10.5,color=INK,leading=None):
    st=ParagraphStyle('p',fontName='CN',fontSize=size,leading=leading or size*1.65,textColor=HexColor(color),wordWrap='CJK')
    p=Paragraph(s,st);_,h=p.wrap(width,H)
    assert y+h<790, (s[:50],y,h)
    p.drawOn(c,x,H-y-h)
    return y+h

def rule(y):
    c.setStrokeColor(HexColor(LINE));c.setLineWidth(.6);c.line(LEFT,H-y,RIGHT,H-y)

def heading(n,title,subtitle):
    c.bookmarkPage('p'+str(n));c.addOutlineEntry(title,'p'+str(n),0,False)
    text(LEFT,27,'NEXIGHT  /  产品使用手册',9,BLUE,True)
    text(RIGHT-123,27,'平台接入 · WorkBuddy',8.5,MUTED)
    rule(51)
    text(LEFT,72,title,23,INK,True)
    para(LEFT,111,subtitle,size=10,color=MUTED)
    rule(794)
    text(LEFT,806,'V1.0  ·  2026-09-11',8,MUTED)
    text(RIGHT-53,806,f'{n:02d} / {page_count:02d}',8,MUTED)
    source_lines.extend(['','## '+title,subtitle,''])

def body(y,s):
    source_lines.append(s.replace('<b>','').replace('</b>','').replace('<br/>','\n'))
    return para(LEFT,y,s)

def callout(y,title,s,height=66,color=BLUE):
    c.setFillColor(HexColor(LIGHT));c.roundRect(LEFT,H-y-height,WIDTH,height,7,fill=1,stroke=0)
    c.setFillColor(HexColor(color));c.rect(LEFT,H-y-height,3,height,fill=1,stroke=0)
    text(LEFT+15,y+10,title,10,color,True)
    if height < 50:
        para(LEFT+90,y+10,s,WIDTH-105,9,leading=14)
    else:
        para(LEFT+15,y+29,s,WIDTH-30,9.3,leading=15)
    source_lines.extend(['> '+title,'> '+s.replace('<br/>',' ')])

def screenshot(name,box,y,maxh,caption,width=WIDTH,x=LEFT):
    if SOURCE != ORIGINALS:
        shutil.copy2(SOURCE/name, ORIGINALS/name)
    src=Image.open(SOURCE/name).convert('RGB')
    if box:
        a,b,d,e=box; src=src.crop((round(a*src.width),round(b*src.height),round(d*src.width),round(e*src.height)))
    path=IMAGES/(name[:-4]+'-manual.jpg');src.save(path,quality=94,subsampling=0)
    scale=min(width/src.width,maxh/src.height)
    dw,dh=src.width*scale,src.height*scale
    px=x+(width-dw)/2
    c.setStrokeColor(HexColor(LINE));c.setFillColor(white)
    c.roundRect(px-1,H-y-dh-1,dw+2,dh+2,5,fill=1,stroke=1)
    c.drawImage(str(path),px,H-y-dh,dw,dh)
    para(x,y+dh+8,caption,width,8.3,MUTED,13)
    source_lines.extend([f'![{caption}](images/{path.name})',''])
    return y+dh+30

def table(y,rows,widths=(120,WIDTH-120),rowh=51):
    for i,(a,b) in enumerate(rows):
        h=rowh
        c.setFillColor(HexColor(LIGHT if i%2==0 else '#F8FAFD'));c.rect(LEFT,H-y-h,WIDTH,h,fill=1,stroke=0)
        para(LEFT+12,y+10,escape(a),widths[0]-22,9.8,INK)
        para(LEFT+widths[0]+10,y+10,b,widths[1]-23,9.5,INK,15)
        source_lines.append(f'- {a}：{b}')
        y+=h
    return y

# 1. Cover with actionable navigation rather than a blank title page.
c.setFillColor(HexColor('#F1F6FC'));c.rect(0,0,W,H,fill=1,stroke=0)
c.setFillColor(HexColor(INK));c.rect(0,H-360,W,360,fill=1,stroke=0)
c.setFillColor(HexColor(TEAL));c.rect(44,H-93,45,4,fill=1,stroke=0)
text(44,45,'NEXIGHT',20,'#FFFFFF',True)
text(44,116,'平台与 WorkBuddy',29,'#FFFFFF',True)
text(44,163,'接入使用手册',29,'#FFFFFF',True)
para(44,224,'从创建 API 密钥到完成第一次模型对话。<br/>适用于平台普通用户与 WorkBuddy 桌面端使用者。',WIDTH,12,'#D7E5F4',22)
text(44,312,'V1.0    /    2026-09-11    /    图文操作指南',10,'#D7E5F4')
c.bookmarkPage('p1');c.addOutlineEntry('使用手册与阅读导航','p1',0,False)
text(44,391,'完成接入，只需四个环节',17,INK,True)
for i,(a,b) in enumerate([('确认模型','模型广场 / 分组'),('创建密钥','名称 / 权限 / 保存'),('连接客户端','地址 / Key / 模型'),('验证使用','测试 / 对话 / 日志')]):
    x=44+i*129
    c.setFillColor(white);c.roundRect(x,H-500,120,66,6,fill=1,stroke=0)
    text(x+10,444,f'0{i+1}  {a}',11,BLUE,True);text(x+10,473,b,8,MUTED)
text(44,535,'阅读导航',13,INK,True)
toc=[('开始前准备与参数速查',2),('进入控制台与密钥管理',3),('确认模型与创建 API 密钥',4),('打开 WorkBuddy 自定义模型',5),('填写连接参数并保存',6),('验证连接与日常使用',7),('常见问题与支持信息',8)]
for j,(title,n) in enumerate(toc):
    y=569+j*24;text(44,y,title,10,INK);text(522,y,f'{n:02d}',10,BLUE,True)
    c.linkRect('', 'p'+str(n),(44,H-y-19,RIGHT,H-y+2),relative=0,thickness=0)
text(44,787,'广州观枢智能科技有限公司  ·  基于 New API 的平台接入指南',8.5,MUTED)
c.showPage()

heading(2,'01  开始前准备','先准备账号、可用模型与客户端，再按图完成接入。')
body(153,'<b>使用条件</b><br/>您已取得 NEXIGHT 平台账号并能正常登录；账号余额与密钥额度足够；电脑已安装 WorkBuddy。本文截图来自 WorkBuddy 5.5.6，其他版本的菜单位置可能略有不同。')
text(LEFT,244,'连接参数速查',14,INK,True)
table(277,[('平台入口','https://gateway.nexus-reach.com'),('接口地址（Base URL）','https://gateway.nexus-reach.com/v1'),('供应商 / 协议','自定义 / OpenAI 兼容协议'),('API Key','在平台创建并复制的完整 API 密钥'),('模型名称','示例：claude-opus-5<br/>以当前平台对您开放的准确模型标识为准'),('密钥分组','示例：default<br/>必须与目标模型可用分组匹配')],rowh=55)
callout(633,'先理解三个名称','API 密钥用于鉴权；模型名称决定调用哪个模型；分组决定可访问的模型范围及适用倍率。',68)
body(727,'截图中的模型列表、价格和账号余额仅为示例，以当前平台配置为准。创建密钥不会自动充值；密钥“无限额度”也不代表账号可免费或无限使用。')
c.showPage()

heading(3,'02  进入控制台','目标：找到 API 密钥管理页，打开创建入口。')
body(150,'<b>步骤 1</b>　访问平台首页并登录，点击顶部“控制台”。')
screenshot('image-20260911175533851.png',(.12,0,.87,.51),181,179,'图 1  首页顶部“控制台”入口。')
body(391,'<b>步骤 2</b>　在左侧菜单点击“API 密钥”。')
screenshot('image-20260911175810315.png',(0,.17,.51,.52),420,160,'图 2  从控制台进入 API 密钥管理。')
body(599,'<b>步骤 3</b>　点击密钥列表右上角“创建 API 密钥”。')
screenshot('image-20260911175838035.png',(.12,.055,1,.242),629,111,'图 3  创建按钮位于页面右上角。')
c.showPage()

heading(4,'03  确认模型并创建密钥','目标：创建一枚能访问目标模型的 API 密钥。')
body(151,'<b>先确认模型</b>　在“模型广场”查看目标模型与可用分组。本文以 <b>claude-opus-5</b> 为例，截图中对应 <b>default</b>；请以您的账号实际可用范围为准。')
screenshot('image-20260911215747703.png',(0,.115,.725,.39),207,130,'图 4  对照左侧分组与模型卡片，复制准确模型名称。')
screenshot('image-20260911214837568.png',(.638,0,1,1),382,320,'图 5  创建面板：填写名称、选择分组并保存。',width=250,x=301)
para(LEFT,386,'<b>1. 填写名称</b><br/>使用便于识别的名称，例如“WorkBuddy-个人”。',235,10.5)
para(LEFT,458,'<b>2. 选择分组</b><br/>选择包含目标模型且账号有权使用的分组。本例为 default。',235,10.5)
para(LEFT,548,'<b>3. 检查额度与权限</b><br/>如设置额度、有效期或模型限制，确认足以完成本次使用。',235,10.5)
para(LEFT,637,'<b>4. 保存并复制</b><br/>点击“保存更改”，创建完成后复制完整 API Key，留待下一步粘贴。',235,10.5)
source_lines.extend(['1. 名称：例如 WorkBuddy-个人。','2. 分组：选择目标模型对应的可用分组。','3. 检查额度、有效期与模型限制。','4. 保存更改，复制完整 API Key。'])
callout(747,'密钥保管','API Key 是访问凭据，请勿放入公开截图、聊天群或共享文档。',40)
c.showPage()

heading(5,'04  打开自定义模型配置','目标：在 WorkBuddy 中找到添加外部模型的入口。')
body(151,'<b>步骤 1</b>　打开 WorkBuddy，进入聊天界面。<br/><b>步骤 2</b>　点击输入框右下方的当前模型名称，展开模型列表。<br/><b>步骤 3</b>　点击列表底部“配置自定义模型”，再选择“添加模型”。')
screenshot('image-20260911215230407.png',(.581,.426,.892,.957),254,371,'图 6  模型选择菜单与“配置自定义模型”入口。')
callout(665,'识别正确入口','列表里已存在的模型名称只是当前选择；请进入“配置自定义模型”添加 NEXIGHT 连接。',70)
body(752,'下一步将填写供应商、接口地址、API Key 和模型名称四个核心字段。')
c.showPage()

heading(6,'05  填写连接参数','目标：完成四个核心字段，测试连接后保存。')
screenshot('image-20260911215431951.png',(.235,.12,.767,.883),152,444,'图 7  添加模型表单：按标注核对四个核心字段。')
body(629,'<b>供应商：</b>选择“自定义”。<br/><b>接口地址：</b>https://gateway.nexus-reach.com/v1<br/><b>API Key：</b>粘贴上一步复制的完整密钥，不添加 Bearer 前缀。<br/><b>模型名称：</b>填入准确标识，本例为 claude-opus-5。')
callout(721,'测试后记得保存','点击“测试连接”；显示成功后点击“保存”。高级能力按实际模型支持情况选择，首次接入可保留默认值。',63)
c.showPage()

heading(7,'06  验证与日常使用','目标：确认配置已保存，并且实际聊天请求能够完成。')
text(LEFT,155,'接入完成的三个判定点',15,INK,True)
table(188,[('01  连接测试通过','WorkBuddy 显示“连接成功”。此项确认当前测试链路可用。'),('02  模型已切换','返回聊天窗口，在模型列表中选中刚刚添加的自定义模型。'),('03  实际对话完成','发送一条简短文本，例如“请用一句话介绍你能做什么”，确认收到正常回复。')],rowh=68)
body(422,'<b>在平台复核</b><br/>进入“控制台 → 使用日志”，核对本次请求的时间、模型名称、状态和消耗。连接测试成功并不等于所有高级能力都已验证；图片输入、工具调用等需要分别按实际场景确认。')
text(LEFT,529,'常用操作',14,INK,True)
table(560,[('切换其他模型','先确认新模型的分组与协议支持，再添加或编辑对应模型配置。'),('查看消耗','在“使用日志”查看请求明细，在“钱包”核对可用余额。'),('停用或更换密钥','在 API 密钥管理中停用旧密钥；新建密钥后同步更新 WorkBuddy。')],rowh=54)
callout(741,'验收清单','连接成功 · 模型选中 · 对话正常 · 日志可查',43,color=TEAL)
c.showPage()

heading(8,'07  常见问题与支持','按“连接参数 → 密钥权限 → 模型可用性”的顺序检查。')
table(153,[('401 / 鉴权失败','检查 API Key 是否完整、是否被停用或过期。表单中不要额外添加 Bearer，也不要粘贴掩码圆点。'),('403 / 权限不足','检查账号状态、密钥模型限制、IP 限制与分组权限；必要时联系平台管理员。'),('404 / 路径或模型错误','Base URL 应为 https://gateway.nexus-reach.com/v1。不要追加 /chat/completions；同时检查模型标识。'),('余额或额度不足','分别核对账号钱包余额与该密钥的可用额度。“无限额度”只取消密钥额度限制。'),('503 / 无可用渠道','核对模型和分组。如错误包含 No available channel，向管理员提供模型名、分组及请求 ID。'),('超时 / 无法连接','检查电脑网络与代理设置。网页能打开不代表模型接口可用；保存具体错误信息后再排查。')],rowh=65)
text(LEFT,578,'联系管理员时，请提供',14,INK,True)
body(608,'发生时间、WorkBuddy 版本、模型名称、密钥分组、错误码或完整错误文本，以及请求 ID（如有）。截图请隐藏 API Key。')
rule(667)
para(LEFT,682,'<b>文档说明</b><br/>依据《02-token连接示例.md》及其 7 张原始截图整理，截图日期为 2026-09-11。本文使用 claude-opus-5 / default 演示操作，不保证模型或价格长期不变。本次编制未重新执行在线连接测试。',WIDTH,9,MUTED,15)
text(LEFT,765,'NEXIGHT 平台基于 New API；WorkBuddy 为第三方客户端。',8.5,MUTED)
c.showPage()
c.save()
(RES/'手册正文.md').write_text('\n\n'.join(source_lines),encoding='utf-8')
if Path(__file__).resolve() != (RES/'生成手册.py').resolve():
    shutil.copy2(__file__,RES/'生成手册.py')
print(json.dumps({'pdf':str(OUT),'bytes':OUT.stat().st_size,'pages':page_count},ensure_ascii=False))
