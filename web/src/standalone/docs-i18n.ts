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

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Minimal i18n for the standalone docs build.
 *
 * The full app config statically imports every locale file (~2.3 MB of JSON);
 * the docs page only needs a few dozen strings. Inlining just those keeps the
 * static bundle small. Missing keys fall back to the English source string.
 */
import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

const en = {
  'API docs': 'API docs',
  'Model API': 'Model API',
  'Seedance 2.0 API': 'Seedance 2.0 API',
  '{{count}} endpoints': '{{count}} endpoints',
  '{{count}} tags': '{{count}} tags',
  'Search APIs': 'Search APIs',
  'Expand All': 'Expand All',
  'Collapse All': 'Collapse All',
  'API overview': 'API overview',
  'No matching endpoints': 'No matching endpoints',
  'Copy endpoint': 'Copy endpoint',
  Deprecated: 'Deprecated',
  Description: 'Description',
  '{{count}} parameters': '{{count}} parameters',
  'Path parameters': 'Path parameters',
  'Query parameters': 'Query parameters',
  'Header parameters': 'Header parameters',
  'Request body': 'Request body',
  'Response body': 'Response body',
  Required: 'Required',
  Optional: 'Optional',
  'Possible values': 'Possible values',
  'Schema is available as a raw object or array.':
    'Schema is available as a raw object or array.',
  'No schema documented.': 'No schema documented.',
  Responses: 'Responses',
  Endpoint: 'Endpoint',
  'Request example': 'Request example',
  'Response example': 'Response example',
  'No request example in this section.': 'No request example in this section.',
  'No response example in this section.':
    'No response example in this section.',
  'Copy example': 'Copy example',
  'No documentation sections found.': 'No documentation sections found.',
  'Copied to clipboard': 'Copied to clipboard',
  'Failed to copy to clipboard': 'Failed to copy to clipboard',
  Docs: 'Docs',
}

const zh = {
  'API docs': '接口文档',
  'Model API': '模型接口',
  'Seedance 2.0 API': 'Seedance 2.0 接口',
  '{{count}} endpoints': '{{count}} 个接口',
  '{{count}} tags': '{{count}} 个分组',
  'Search APIs': '搜索接口',
  'Expand All': '全部展开',
  'Collapse All': '全部收起',
  'API overview': '接口总览',
  'No matching endpoints': '没有匹配的接口',
  'Copy endpoint': '复制接口',
  Deprecated: '已弃用',
  Description: '说明信息',
  '{{count}} parameters': '{{count}} 个参数',
  'Path parameters': '路径参数',
  'Query parameters': '查询参数',
  'Header parameters': '请求头参数',
  'Request body': '请求体',
  'Response body': '响应体',
  Required: '必需',
  Optional: '可选',
  'Possible values': '可选值',
  'Schema is available as a raw object or array.': '该结构为原始对象或数组。',
  'No schema documented.': '暂无结构说明。',
  Responses: '响应',
  Endpoint: '端点',
  'Request example': '请求示例',
  'Response example': '响应示例',
  'No request example in this section.': '当前接口暂无请求示例。',
  'No response example in this section.': '当前接口暂无响应示例。',
  'Copy example': '复制示例',
  'No documentation sections found.': '未找到文档内容。',
  'Copied to clipboard': '已复制到剪贴板',
  'Failed to copy to clipboard': '复制到剪贴板失败',
  Docs: '文档',
}

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      zh: { translation: zh },
    },
    fallbackLng: 'en',
    interpolation: { escapeValue: false },
  })

export default i18n
