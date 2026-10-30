/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/

/**
 * Curated model-square copy supplied with the model classification reference.
 * This is presentation metadata only: live pricing, routing and availability
 * continue to come from /api/pricing.
 */
export type ModelCatalogMetadata = {
  category: string
  tags: string[]
  strengths: string[]
}

const catalog = {
  'wan3.0-video': [
    '视频生成 · 文生/图生视频',
    ['视频', 'T2V/I2V'],
    [
      '文本/图片生成短视频',
      '多分辨率（480P/720P/1080P）可选',
      '电商/营销短视频素材',
    ],
  ],
  'doubao-seedance-2.5': [
    '视频生成 · Seedance 2.5',
    ['视频', 'Seedance'],
    ['文生视频 / 图生视频', '运动一致性与镜头稳定性', '电商、广告、短剧素材'],
  ],
  'MiniMax-H3-Sensitive': [
    '视频生成 · H3（敏感/可控）',
    ['视频', '多模态', '2K'],
    [
      '通用多模态视频生成',
      '2K 直出、原生音画同步',
      '视频编辑 / 动作迁移(V2V)、复杂指令遵循',
    ],
  ],
  'doubao-seedance-2.0-mini': [
    '视频生成 · Seedance 2.0 mini',
    ['视频', 'Seedance', '轻量'],
    ['轻量视频生成', '快速出片、低成本', '草稿与批量素材'],
  ],
  'doubao-seedance-2-0-260128': [
    '视频生成 · Seedance 2.0',
    ['视频', 'Seedance'],
    ['文生/图生视频', '人物与商品一致性', '营销短视频'],
  ],
  'MiniMax-H3-2K': [
    '视频生成 · H3 2K 高清',
    ['视频', '多模态', '2K', '高清'],
    ['2K 最高分辨率视频直出', '商用级画质', '高精度广告/影视预演'],
  ],
  'MiniMax-H3-261013': [
    '视频生成 · H3（版本 261013）',
    ['视频', '多模态'],
    [
      '通用多模态视频生成',
      '2K、最长 15 秒音画内容',
      '多语言（11 种）、指令遵循强',
    ],
  ],
  'doubao-seedance-2-0-fast-260128': [
    '视频生成 · Seedance fast',
    ['视频', 'Seedance', '高速'],
    ['高速视频生成', '低延迟出片', '实时/高并发场景'],
  ],
  'MiniMax-H3': [
    '视频生成 · H3 通用旗舰',
    ['视频', '多模态', '旗舰'],
    [
      '统一多模态理解与生成',
      '文/图/音频/视频输入',
      '视频编辑能力（榜单全球第一）',
    ],
  ],
  'doubao-seedance-2.0': [
    '视频生成 · Seedance 2.0',
    ['视频', 'Seedance'],
    ['通用视频生成', '文生/图生视频', '商业短片'],
  ],
  'wan3.0-video-prime': [
    '视频生成 · 增强版',
    ['视频', 'Prime'],
    ['在 wan3.0-video 基础上的高清增强版', '更长/更高质感视频', '广告级短片'],
  ],
  'HappyHorse1.1': [
    '视频生成 · 1.1',
    ['视频', '音画同步', '唇形对齐'],
    [
      '文生/图生/参考生视频',
      '原生音画同步 + 7 语言唇形对齐',
      '电商广告、短剧、品牌营销、游戏 CG',
    ],
  ],
  'doubao-seedream-5-0-260128': [
    '图像生成 · Seedream 5.0',
    ['图像', 'Seedream'],
    ['文生图 / 图像生成', '高质感商业图像', '电商、广告、品牌视觉'],
  ],
  'qwen-image-3.0': [
    '图像生成 · 万相 3.0',
    ['图像', 'T2I'],
    ['文生图 / 图像编辑', '高质量中文语义图像', '海报、电商主图、创意设计'],
  ],
  'qwen-image-2.0': [
    '图像生成 · 万相 2.0',
    ['图像'],
    ['文生图 / 图像生成', '通用创意图像', '设计素材'],
  ],
  'doubao-seedream-5-0-pro-260628': [
    '图像生成 · Seedream 5.0 Pro',
    ['图像', 'Seedream', 'Pro'],
    ['高质量图像生成（Pro）', '精细质感与细节', '商用级视觉物料'],
  ],
  'doubao-seedream-5-0-flash-260915': [
    '图像生成 · Seedream 5.0 Flash',
    ['图像', 'Seedream', 'Flash'],
    ['高速图像生成（Flash）', '低延迟出图', '批量/实时素材'],
  ],
  'qwen3.8-flash': [
    '文本大模型 · 轻量高速',
    ['文本', '代码', 'Agent', '高速'],
    [
      '通用对话、中文理解',
      '代码生成、Function Calling / 工具调用',
      'Agent 编排、长上下文、低延迟高并发',
    ],
  ],
  'deepseek-v4.1-flash': [
    '文本大模型 · 轻量高速（v4.1）',
    ['文本', '代码', '推理', '高速'],
    ['代码、数学推理', '通用对话', '高性价比'],
  ],
  'deepseek-v4-flash': [
    '文本大模型 · 轻量高速',
    ['文本', '代码', '推理', '高速'],
    ['代码、数学推理', '通用任务', '高性价比、低延迟'],
  ],
  'kimi-k3': [
    '文本大模型 · 超长上下文',
    ['文本', '长上下文', '综述'],
    [
      '超长上下文（长文档/论文/书籍）',
      '长文本理解与综述',
      '资料分析、知识密集型任务',
    ],
  ],
  'doubao-seed-2.1-lite': [
    '文本大模型 · 轻量',
    ['文本', '轻量', '中文'],
    ['中文通用对话', '高性价比、实时交互', '客服、陪伴、轻量助手'],
  ],
  'glm-5.3': [
    '文本大模型 · 通用',
    ['文本', '代码', 'Agent'],
    ['通用对话、中文', '代码、Agent / 工具调用', '复杂任务规划'],
  ],
  'doubao-seed-2-0-mini-260428': [
    '文本大模型 · mini',
    ['文本', 'mini', '高速'],
    ['轻量高速对话', '低成本实时交互', '嵌入式/高并发文本场景'],
  ],
  'deepseek-v4-pro': [
    '文本大模型 · 旗舰',
    ['文本', '推理', '代码', '旗舰'],
    ['深度推理与长链思考', '代码、数学、复杂问题求解', '质量天花板、专业任务'],
  ],
  'glm-5.3-flash': [
    '文本大模型 · 轻量高速',
    ['文本', '代码', '高速'],
    ['通用对话、代码', '低延迟、低成本', '高并发场景'],
  ],
  'openrouter/free': [
    '文本大模型 · 免费路由',
    ['文本', '免费', '路由'],
    [
      '聚合免费 LLM 路由',
      '低成本试用与原型验证',
      '通用对话（质量随路由模型波动）',
    ],
  ],
  'qwen3.8-max': [
    '文本大模型 · 旗舰',
    ['文本', '推理', '旗舰'],
    ['复杂推理、数学与代码', '长文本高质量生成', '对质量上限要求高的任务'],
  ],
  'Hunyuan/hy3': [
    '文本大模型 · 通用',
    ['文本', '中文', '企业'],
    ['中文通用生成', '内容/营销文案、社交场景', '企业级应用'],
  ],
} as const

export function getModelCatalogMetadata(
  modelName?: string
): ModelCatalogMetadata | undefined {
  if (!modelName) return undefined
  const item = catalog[modelName as keyof typeof catalog]
  if (!item) return undefined
  const [category, tags, strengths] = item
  return { category, tags: [...tags], strengths: [...strengths] }
}

export function getAllModelCatalogMetadata(): Record<
  string,
  ModelCatalogMetadata
> {
  return Object.fromEntries(
    Object.keys(catalog).map((modelName) => [
      modelName,
      getModelCatalogMetadata(modelName),
    ])
  ) as Record<string, ModelCatalogMetadata>
}
