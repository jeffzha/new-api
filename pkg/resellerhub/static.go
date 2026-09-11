package resellerhub

// EmbeddedIndexHTML returns the standalone Reseller Hub management interface.
// The page intentionally has no external runtime or asset dependencies.
func EmbeddedIndexHTML() string {
	return embeddedIndexHTML
}

const embeddedIndexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="light">
  <title>Reseller Hub</title>
  <style>
    :root {
      --bg: #f4f6f8;
      --panel: #fff;
      --panel-soft: #f8fafb;
      --text: #17212b;
      --muted: #687381;
      --line: #dce2e8;
      --line-strong: #c9d2dc;
      --accent: #087f5b;
      --accent-dark: #06664a;
      --accent-soft: #e7f6ef;
      --blue: #2563a8;
      --blue-soft: #eaf2fb;
      --amber: #9a6700;
      --amber-soft: #fff5d9;
      --danger: #b42318;
      --danger-dark: #8f1c13;
      --danger-soft: #fff0ee;
      --shadow: 0 1px 2px rgba(16,24,40,.05), 0 8px 24px rgba(16,24,40,.04);
      --sidebar: 232px;
    }
    * { box-sizing: border-box; }
    html, body { min-height: 100%; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--text);
      font: 14px/1.5 Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      letter-spacing: 0;
    }
    button, input, select, textarea { font: inherit; letter-spacing: 0; }
    button, select { cursor: pointer; }
    button:disabled { cursor: not-allowed; opacity: .52; }
    a { color: inherit; }
    .app { min-height: 100vh; display: grid; grid-template-columns: var(--sidebar) minmax(0,1fr); }
    .sidebar {
      position: sticky; top: 0; height: 100vh; overflow-y: auto;
      background: #111923; color: #e8edf2; border-right: 1px solid #27313d;
      padding: 18px 12px;
    }
    .brand { display: flex; align-items: center; gap: 10px; padding: 3px 8px 18px; border-bottom: 1px solid #2a3541; }
    .brand-mark { width: 31px; height: 31px; display: grid; place-items: center; border-radius: 6px; background: #24a77c; color: #071a14; font-weight: 800; }
    .brand strong { display: block; font-size: 15px; }
    .brand small { color: #9eabb8; display: block; margin-top: 1px; }
    .nav-section { margin-top: 18px; }
    .nav-caption { padding: 0 10px 7px; color: #7f8c99; font-size: 11px; font-weight: 700; text-transform: uppercase; }
    .nav-button {
      width: 100%; min-height: 38px; display: flex; align-items: center; gap: 10px;
      border: 0; border-radius: 6px; background: transparent; color: #c6d0da; padding: 8px 10px; text-align: left;
    }
    .nav-button:hover { background: #1b2632; color: #fff; }
    .nav-button.active { background: #233e36; color: #eafff7; box-shadow: inset 3px 0 #38c796; }
    .nav-icon { width: 18px; text-align: center; color: #8fa0af; font-size: 12px; font-weight: 800; }
    .nav-button.active .nav-icon { color: #62deb3; }
    .main { min-width: 0; }
    .topbar {
      height: 58px; position: sticky; top: 0; z-index: 20; display: flex; align-items: center; justify-content: space-between;
      background: rgba(255,255,255,.95); border-bottom: 1px solid var(--line); padding: 0 24px; backdrop-filter: blur(8px);
    }
    .topbar-left { display: flex; align-items: center; gap: 10px; min-width: 0; }
    .mobile-menu { display: none; }
    .breadcrumb { color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .topbar-right { display: flex; align-items: center; gap: 10px; }
    .identity { text-align: right; min-width: 0; }
    .identity strong { display: block; max-width: 210px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 13px; }
    .identity span { color: var(--muted); font-size: 11px; }
    .segmented { display: inline-flex; background: #edf1f4; padding: 2px; border-radius: 6px; }
    .segmented button { min-width: 36px; height: 28px; border: 0; border-radius: 4px; background: transparent; color: var(--muted); }
    .segmented button.active { background: #fff; color: var(--text); box-shadow: 0 1px 3px rgba(16,24,40,.12); }
    .content { max-width: 1540px; margin: 0 auto; padding: 24px; }
    .page-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
    .page-head h1 { margin: 0; font-size: 22px; line-height: 1.25; }
    .page-head p { margin: 5px 0 0; color: var(--muted); }
    .actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .button {
      min-height: 34px; border: 1px solid var(--line-strong); border-radius: 6px; background: #fff; color: var(--text);
      padding: 7px 12px; font-weight: 650; white-space: nowrap;
    }
    .button:hover { border-color: #96a3b1; background: #fafbfc; }
    .button.primary { border-color: var(--accent); background: var(--accent); color: #fff; }
    .button.primary:hover { background: var(--accent-dark); }
    .button.danger { border-color: #efb6b0; background: #fff; color: var(--danger); }
    .button.danger:hover { background: var(--danger-soft); border-color: var(--danger); }
    .button.quiet { border-color: transparent; background: transparent; color: var(--blue); padding-inline: 7px; }
    .button.compact { min-height: 29px; padding: 4px 8px; font-size: 12px; }
    .icon-button { width: 34px; height: 34px; padding: 0; display: grid; place-items: center; }
    .grid { display: grid; gap: 14px; }
    .metrics { grid-template-columns: repeat(4,minmax(0,1fr)); margin-bottom: 16px; }
    .metric { background: var(--panel); border: 1px solid var(--line); border-radius: 7px; padding: 16px; box-shadow: var(--shadow); min-width: 0; }
    .metric-label { color: var(--muted); font-size: 12px; }
    .metric-value { margin-top: 7px; font-size: 22px; line-height: 1.2; font-weight: 760; overflow-wrap: anywhere; }
    .metric-note { color: var(--muted); font-size: 11px; margin-top: 5px; }
    .panel { background: var(--panel); border: 1px solid var(--line); border-radius: 7px; box-shadow: var(--shadow); }
    .panel + .panel { margin-top: 14px; }
    .panel-head { min-height: 50px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 11px 14px; border-bottom: 1px solid var(--line); }
    .panel-head h2 { margin: 0; font-size: 14px; }
    .panel-head p { margin: 2px 0 0; color: var(--muted); font-size: 12px; }
    .panel-body { padding: 14px; }
    .filterbar { display: flex; align-items: end; gap: 10px; flex-wrap: wrap; padding: 12px 14px; border-bottom: 1px solid var(--line); background: var(--panel-soft); }
    .filterbar .field { min-width: 150px; flex: 0 1 220px; }
    .filterbar .field.grow { flex: 1 1 260px; }
    .field { display: grid; gap: 5px; min-width: 0; }
    .field label { font-size: 12px; font-weight: 650; color: #344054; }
    .required { color: var(--danger); margin-left: 3px; }
    .help { color: var(--muted); font-size: 11px; }
    input, select, textarea {
      width: 100%; min-height: 36px; border: 1px solid var(--line-strong); border-radius: 5px; background: #fff; color: var(--text); padding: 7px 9px; outline: none;
    }
    textarea { min-height: 78px; resize: vertical; }
    input:focus, select:focus, textarea:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(8,127,91,.1); }
    .form-grid { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 13px; }
    .form-grid .wide { grid-column: 1/-1; }
    .table-shell { width: 100%; overflow-x: auto; -webkit-overflow-scrolling: touch; }
    table { width: 100%; min-width: 900px; border-collapse: collapse; }
    th, td { padding: 10px 12px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: middle; white-space: nowrap; }
    th { position: sticky; top: 0; z-index: 1; background: #f8fafb; color: #52606d; font-size: 11px; text-transform: uppercase; font-weight: 750; }
    tbody tr:hover { background: #fbfcfd; }
    tbody tr:last-child td { border-bottom: 0; }
    td.wrap { min-width: 220px; max-width: 420px; white-space: normal; overflow-wrap: anywhere; }
    .row-actions { display: flex; gap: 3px; align-items: center; }
    .badge { display: inline-flex; align-items: center; min-height: 23px; padding: 2px 7px; border-radius: 999px; background: #eef2f5; color: #475467; font-size: 11px; font-weight: 700; }
    .badge.active, .badge.enabled, .badge.success, .badge.applied { background: var(--accent-soft); color: #066044; }
    .badge.suspended, .badge.disabled, .badge.failed, .badge.closed, .badge.retired { background: var(--danger-soft); color: var(--danger); }
    .badge.retiring, .badge.pending, .badge.prepared, .badge.reconcile_required { background: var(--amber-soft); color: var(--amber); }
    .badge.viewer { background: var(--blue-soft); color: var(--blue); }
    .mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; }
    .amount { font-variant-numeric: tabular-nums; font-weight: 700; }
    .amount.negative { color: var(--danger); }
    .subvalue { display: block; margin-top: 2px; color: var(--muted); font-size: 11px; font-weight: 400; }
    .empty { padding: 44px 18px; text-align: center; color: var(--muted); }
    .empty strong { display: block; color: #344054; margin-bottom: 4px; }
    .notice { border: 1px solid #e2cc92; background: var(--amber-soft); color: #6f4c00; border-radius: 6px; padding: 10px 12px; margin-bottom: 14px; }
    .notice.danger { border-color: #efb6b0; background: var(--danger-soft); color: var(--danger); }
    .notice.info { border-color: #b9d2ec; background: var(--blue-soft); color: #174f87; }
    .detail-list { display: grid; grid-template-columns: 160px minmax(0,1fr); border-top: 1px solid var(--line); }
    .detail-list dt, .detail-list dd { margin: 0; padding: 9px 12px; border-bottom: 1px solid var(--line); }
    .detail-list dt { background: var(--panel-soft); color: var(--muted); font-size: 12px; }
    .detail-list dd { overflow-wrap: anywhere; }
    .modal-backdrop { position: fixed; inset: 0; z-index: 100; display: none; align-items: center; justify-content: center; background: rgba(16,24,40,.52); padding: 18px; }
    .modal-backdrop.open { display: flex; }
    .modal { width: min(680px,100%); max-height: calc(100vh - 36px); display: flex; flex-direction: column; background: #fff; border-radius: 8px; box-shadow: 0 24px 64px rgba(16,24,40,.28); }
    .modal.small { width: min(480px,100%); }
    .modal-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--line); }
    .modal-head h2 { margin: 0; font-size: 16px; }
    .modal-body { padding: 16px; overflow-y: auto; }
    .modal-foot { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--line); }
    .secret-box { border: 1px solid #b9d2ec; background: #f4f9ff; border-radius: 6px; padding: 12px; }
    .secret-value { display: flex; gap: 8px; margin-top: 8px; }
    .secret-value input { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
    .toast-stack { position: fixed; z-index: 150; top: 70px; right: 18px; display: grid; gap: 8px; width: min(380px,calc(100vw - 36px)); }
    .toast { background: #17212b; color: #fff; border-radius: 6px; padding: 11px 13px; box-shadow: 0 12px 32px rgba(16,24,40,.24); animation: slidein .18s ease-out; }
    .toast.error { background: #8f1c13; }
    .toast.success { background: #076044; }
    .loading { min-height: 180px; display: grid; place-items: center; color: var(--muted); }
    .spinner { width: 22px; height: 22px; border: 3px solid #dbe4ea; border-top-color: var(--accent); border-radius: 50%; animation: spin .7s linear infinite; }
    .overlay { display: none; }
    @keyframes spin { to { transform: rotate(360deg); } }
    @keyframes slidein { from { transform: translateY(-8px); opacity: 0; } }
    @media (max-width: 1050px) { .metrics { grid-template-columns: repeat(2,minmax(0,1fr)); } }
    @media (max-width: 760px) {
      .app { display: block; }
      .sidebar { position: fixed; left: 0; top: 0; z-index: 60; width: var(--sidebar); transform: translateX(-100%); transition: transform .18s ease; }
      body.menu-open .sidebar { transform: translateX(0); }
      .overlay { position: fixed; inset: 0; z-index: 50; background: rgba(16,24,40,.42); }
      body.menu-open .overlay { display: block; }
      .mobile-menu { display: grid; }
      .topbar { padding: 0 12px; }
      .content { padding: 16px 12px; }
      .identity { display: none; }
      .page-head { align-items: stretch; flex-direction: column; }
      .metrics { grid-template-columns: 1fr; }
      .form-grid { grid-template-columns: 1fr; }
      .detail-list { grid-template-columns: 1fr; }
      .detail-list dt { border-bottom: 0; padding-bottom: 2px; }
      .detail-list dd { padding-top: 2px; }
    }
  </style>
</head>
<body>
  <div class="app">
    <aside class="sidebar" aria-label="Primary navigation">
      <div class="brand">
        <div class="brand-mark">RH</div>
        <div><strong>Reseller Hub</strong><small id="roleLabel">Loading...</small></div>
      </div>
      <nav id="navigation" class="nav-section"></nav>
    </aside>
    <div class="overlay" id="overlay"></div>
    <main class="main">
      <header class="topbar">
        <div class="topbar-left">
          <button class="button icon-button mobile-menu" id="menuButton" type="button" aria-label="Menu">☰</button>
          <div class="breadcrumb" id="breadcrumb">Reseller Hub</div>
        </div>
        <div class="topbar-right">
          <button class="button icon-button" id="refreshButton" type="button" title="Refresh" aria-label="Refresh">↻</button>
          <div class="segmented" aria-label="Language">
            <button type="button" data-language="zh">中</button>
            <button type="button" data-language="en">EN</button>
          </div>
          <div class="identity"><strong id="identityName">-</strong><span id="identityRole">-</span></div>
        </div>
      </header>
      <div class="content" id="content"><div class="loading"><div class="spinner"></div></div></div>
    </main>
  </div>

  <div class="modal-backdrop" id="formModal" role="dialog" aria-modal="true" aria-labelledby="formModalTitle">
    <div class="modal">
      <div class="modal-head"><h2 id="formModalTitle"></h2><button class="button icon-button" type="button" data-close-modal aria-label="Close">×</button></div>
      <form id="modalForm">
        <div class="modal-body" id="formModalBody"></div>
        <div class="modal-foot"><button class="button" type="button" data-close-modal id="cancelButton">Cancel</button><button class="button primary" type="submit" id="submitButton">Save</button></div>
      </form>
    </div>
  </div>

  <div class="modal-backdrop" id="confirmModal" role="alertdialog" aria-modal="true" aria-labelledby="confirmTitle">
    <div class="modal small">
      <div class="modal-head"><h2 id="confirmTitle"></h2><button class="button icon-button" type="button" data-close-confirm aria-label="Close">×</button></div>
      <div class="modal-body" id="confirmBody"></div>
      <div class="modal-foot"><button class="button" type="button" data-close-confirm id="confirmCancel">Cancel</button><button class="button danger" type="button" id="confirmButton">Confirm</button></div>
    </div>
  </div>

  <div class="modal-backdrop" id="secretModal" role="dialog" aria-modal="true" aria-labelledby="secretTitle">
    <div class="modal small">
      <div class="modal-head"><h2 id="secretTitle">API Key</h2></div>
      <div class="modal-body">
        <div class="notice danger" id="secretWarning"></div>
        <div class="secret-box"><div id="secretLabel"></div><div class="secret-value"><input id="secretValue" readonly aria-label="API Key"><button class="button" id="copySecret" type="button"></button></div></div>
      </div>
      <div class="modal-foot"><button class="button primary" type="button" id="closeSecret"></button></div>
    </div>
  </div>
  <div class="toast-stack" id="toasts" aria-live="polite"></div>

  <script>
  (function () {
    'use strict';

    var API = '/reseller/api';
    var state = {
      lang: localStorage.getItem('reseller_hub_language') || 'zh',
      me: null,
      isRoot: false,
      canWrite: false,
      page: 'dashboard',
      csrf: '',
      conversion: { currency: 'USD', symbol: '$', quota_per_unit: 500000, usd_exchange_rate: 1 },
      reference: { users: [], groups: [], models: [], limits: { min_discount_bps: 100, max_discount_bps: 50000 } },
      resellers: [],
      customers: [],
      selectedResellerId: '',
      selectedCustomerId: ''
    };

    var messages = {
      zh: {
        role_root: '平台超级管理员', role_reseller: '代理商工作台', role_viewer: '代理商只读成员',
        nav_workspace: '工作区', dashboard: '总览', resellers: '代理商', members: '成员', customers: '客户', discounts: '计费比例', keys: '客户 API Key', quota: '额度调整', ledger: '额度账本', usage: '用量', audit: '审计',
        refresh: '刷新', add: '增加', subtract: '减少', reverse: '冲正', create: '创建', edit: '编辑', enable: '启用', disable: '停用', retire: '退役', remove: '移除', view: '查看', save: '保存', cancel: '取消', confirm: '确认', copy: '复制', copied: '已复制', search: '搜索', all: '全部', apply: '应用',
        loading: '正在加载', no_data: '暂无数据', no_data_hint: '调整筛选条件或创建第一条记录。', operation_success: '操作成功', request_failed: '请求失败', required: '必填',
        dashboard_title: '运营总览', dashboard_desc_root: '查看所有代理商、客户和额度运行状态。', dashboard_desc_reseller: '查看本代理商客户、Key、余额和近期活动。',
        reseller_count: '代理商数', customer_count: '客户数', active_key_count: 'Active Key', negative_count: '负额度 Key', carrier_balance: '共享承载余额', managed_balance: '客户 Key 余额合计', month_usage: '本月用量', recent_activity: '最近活动', operational_alerts: '运行预警', no_alerts: '当前没有需要处理的预警。', alert_negative_balance: '存在负额度客户 Key', alert_managed_exceeds_carrier: '客户 Key 余额合计高于共享承载余额', alert_stale_reconciliation: '额度事件超过容忍窗口仍未收敛', alert_carrier_low: '共享承载余额低于预警线', alert_key_capacity_warning: '所属账号 Key 数已达到 MaxUserTokens 的 80%', alert_key_capacity_blocked: '所属账号 Key 数已达到 MaxUserTokens 上限', alert_key_qps: '单个 Key 最近一分钟请求速率超过预警线',
        reseller_management: '代理商管理', reseller_desc: '创建代理商、绑定额度承载账号并维护默认计费比例。', new_reseller: '新建代理商', reseller_name: '代理商名称', reseller_code: '代理商编码', reseller_code_hint: '系统创建时自动生成，作为稳定唯一标识，创建后不可修改。', status: '状态', default_discount: '默认计费比例（%）', carrier_user: '额度承载账号 ID', carrier_user_hint: '普通用户账号，一个账号只能作为一个代理商的额度承载账号。', created_at: '创建时间', updated_at: '更新时间', actions: '操作',
        active: '正常', suspended: '已停用', closed: '已关闭', enabled: '已启用', disabled: '已停用', retiring: '退役中', retired: '已退役',
        member_management: '成员管理', member_desc: '把现有登录账号绑定到代理商，并授予管理员或只读角色。', select_reseller: '选择代理商', select_user: '选择账号', select_group: '选择分组', add_member: '添加成员', member_requires_reseller: '请先创建代理商；有代理商后，选择代理商即可添加成员。', user_id: '用户 ID', member_role: 'Sidecar 角色', reseller_admin: '代理商管理员', reseller_viewer: '只读成员', username: '用户名',
        customer_management: '客户管理', customer_desc: '客户不创建独立登录账号，每个客户第一期只允许一个 Active Key。', new_customer: '新建客户', customer_name: '客户名称', external_ref: '外部客户编号（选填）', external_ref_help: '用于关联你在 CRM、ERP 或合同系统中的客户编号；同一代理商内不可重复，可留空。', customer_requires_reseller: '请先创建并选择代理商，然后才能新建客户。', customer_discount: '客户计费比例（%）', inherit_discount: '继承代理商默认计费比例', effective_discount: '有效计费比例',
        setup_guide: '首次配置流程', setup_guide_desc: '当前还没有代理商数据，请按顺序完成以下四步。', setup_step_reseller: '1. 创建代理商并选择额度承载账号', setup_step_member: '2. 将现有登录账号添加为代理商管理员或只读成员', setup_step_customer: '3. 在代理商下创建客户并设置计费比例', setup_step_key: '4. 为客户创建受额度控制的 API Key', start_setup: '开始创建代理商',
        discount_management: '计费比例管理', discount_desc: '客户计费比例优先于代理商默认计费比例，历史版本不会被覆盖。', select_customer: '选择客户', set_discount: '设置计费比例', discount_bps: '计费比例（%）', discount_help: '填写按原价计费的百分比：100 = 原价，85 = 原价的 85%（8.5 折），120 = 原价的 1.2 倍；留空表示继承代理商默认计费比例。', allowed_range: '允许范围', effective_at: '生效时间', source: '来源', reason: '原因', history: '历史版本',
        key_management: '客户 API Key 管理', key_desc: '为客户签发受额度控制的 API Key。完整 Key 仅在创建成功后显示一次。', key_rules: '额度与数量规则', finite_key_explanation: '额度模式固定为受控额度（unlimited_quota=false），余额小于等于 0 后停止新调用；每个客户最多 1 个 Active 或 Retiring Key；所属账号的 Key 总数还受系统设置 MaxUserTokens 约束。上述限制只影响新调用，在途任务仍会完成结算。', max_user_tokens_rule: '当前所属账号 Key 总上限', carrier_token_usage: '所属账号当前 Key 数', allocation_warning: 'Key 数量已达到系统上限的 80%，请联系平台管理员规划容量。', allocation_blocked: '当前不能创建新客户 Key', create_key: '创建客户 API Key', key_name: 'Key 名称', token_id: 'Token ID', key_preview: 'Key 标识', remain_quota: '剩余额度', used_quota: '已用额度', group: '分组', models: '模型范围', models_help: '可从系统现有模型中多选；留空表示不设置模型白名单。', expires_at: '到期时间', initial_quota: '初始额度',
        one_time_key: '仅显示一次的 API Key', one_time_warning: '请立即安全保存。关闭后 Reseller Hub 不会再次显示完整 Key，也不会写入浏览器存储。', saved_key: '我已安全保存',
        quota_management: '额度增加与减少', quota_desc: '人工调整只改变 remain_quota，不改变 used_quota；不提供覆盖操作。', input_unit: '输入单位', display_currency: '当前币种金额', raw_quota: '原始 quota', amount: '调整数量', current_balance: '当前额度', change_amount: '本次变化', after_balance: '调整后额度', standard_amount: '标准金额', discounted_amount: '客户结算金额', conversion_snapshot: '换算快照', in_flight_warning: '在途调用和异步任务仍可能使最终余额低于此预览值；余额小于等于零后停止新调用。', subtract_guard: '人工减少不得把执行时可确认余额扣成负数。', idempotency: '本次操作使用幂等键，重复提交不会重复生效。',
        quota_ledger: '额度账本', ledger_desc: '查看每次增减的操作者、前后余额、币种与计费比例快照。', event_id: '事件 ID', operation: '操作', before: '调整前', delta: '变化', after: '调整后', actor: '操作者', request_id: '请求 ID', ledger_status: '账本状态', snapshot_amount: '操作时金额', current_amount: '当前重算金额',
        usage_title: '用量查询', usage_desc: '按客户、Key、模型和时间查看标准消耗与客户结算金额。', start_time: '开始时间', end_time: '结束时间', model: '模型', standard_usage: '标准消耗', prompt_tokens: '输入 Token', completion_tokens: '输出 Token', channel_id: '渠道 ID', log_id: '日志 ID',
        audit_title: '审计日志', audit_desc: '追踪代理商、成员、客户、计费比例、Key 和额度操作。', action: '动作', target: '目标', detail: '详情', ip: 'IP 地址',
        funding: '增加承载额度', funding_title: '增加代理商共享承载额度', funding_note: 'Root 第一期只允许增加 users.quota，不允许减少或覆盖。',
        confirm_danger: '确认高风险操作', confirm_disable_customer: '停用客户会阻止该客户 Key 的新调用；在途任务仍会继续结算。', confirm_disable_key: '停用 Key 会阻止新调用；在途任务仍会继续结算。', confirm_retire_key: '退役后 Key 不再接受新调用。存在未完成异步任务时将进入退役中状态。', confirm_remove_member: '移除后该账号将无法继续访问此代理商数据。', confirm_subtract: '请再次核对减少额度、客户和原因。此操作会写入不可覆盖的审计账本。', confirm_reverse: '冲正会创建一条方向相反、数量相同的新账本记录，并引用原事件；不会删除或覆盖历史。',
        currency_config: '币种配置', quota_per_unit: '每单位 quota', exchange_rate: '有效汇率', discount: '计费比例', page_unavailable: '当前身份无权访问此页面。', login_expired: '登录已失效，请返回主系统重新登录。', membership_required: '主系统登录有效，但该账号尚未被授权为代理商成员。请让超级管理员在“成员”页面绑定该账号。', reseller_inactive: '该账号所属的代理商已停用，请联系超级管理员。'
      },
      en: {
        role_root: 'Platform super administrator', role_reseller: 'Reseller workspace', role_viewer: 'Reseller read-only member',
        nav_workspace: 'Workspace', dashboard: 'Overview', resellers: 'Resellers', members: 'Members', customers: 'Customers', discounts: 'Billing percentages', keys: 'Customer API keys', quota: 'Quota adjustments', ledger: 'Quota ledger', usage: 'Usage', audit: 'Audit',
        refresh: 'Refresh', add: 'Add', subtract: 'Subtract', reverse: 'Reverse', create: 'Create', edit: 'Edit', enable: 'Enable', disable: 'Disable', retire: 'Retire', remove: 'Remove', view: 'View', save: 'Save', cancel: 'Cancel', confirm: 'Confirm', copy: 'Copy', copied: 'Copied', search: 'Search', all: 'All', apply: 'Apply',
        loading: 'Loading', no_data: 'No data', no_data_hint: 'Change the filters or create the first record.', operation_success: 'Operation completed', request_failed: 'Request failed', required: 'Required',
        dashboard_title: 'Operations overview', dashboard_desc_root: 'Review all resellers, customers, and quota health.', dashboard_desc_reseller: 'Review your customers, keys, balances, and recent activity.',
        reseller_count: 'Resellers', customer_count: 'Customers', active_key_count: 'Active keys', negative_count: 'Negative-balance keys', carrier_balance: 'Shared carrier balance', managed_balance: 'Managed key balance', month_usage: 'Monthly usage', recent_activity: 'Recent activity', operational_alerts: 'Operational alerts', no_alerts: 'No operational alerts require attention.', alert_negative_balance: 'Customer keys have negative balances', alert_managed_exceeds_carrier: 'Managed key balances exceed the shared carrier balance', alert_stale_reconciliation: 'Quota events remain unreconciled beyond the grace window', alert_carrier_low: 'Shared carrier balance is below its warning threshold', alert_key_capacity_warning: 'Carrier key count reached 80% of MaxUserTokens', alert_key_capacity_blocked: 'Carrier key count reached the MaxUserTokens limit', alert_key_qps: 'A key exceeded its recent one-minute request-rate threshold',
        reseller_management: 'Reseller management', reseller_desc: 'Create resellers, bind quota carrier accounts, and maintain default billing percentages.', new_reseller: 'New reseller', reseller_name: 'Reseller name', reseller_code: 'Reseller code', reseller_code_hint: 'Generated automatically at creation as a stable unique identifier. It cannot be changed later.', status: 'Status', default_discount: 'Default billing percentage (%)', carrier_user: 'Quota carrier user ID', carrier_user_hint: 'A standard user account that may serve as the quota carrier for only one reseller.', created_at: 'Created', updated_at: 'Updated', actions: 'Actions',
        active: 'Active', suspended: 'Suspended', closed: 'Closed', enabled: 'Enabled', disabled: 'Disabled', retiring: 'Retiring', retired: 'Retired',
        member_management: 'Member management', member_desc: 'Bind existing login accounts to a reseller as administrators or read-only members.', select_reseller: 'Select reseller', select_user: 'Select account', select_group: 'Select group', add_member: 'Add member', member_requires_reseller: 'Create a reseller first. Once one exists, select it to add members.', user_id: 'User ID', member_role: 'Sidecar role', reseller_admin: 'Reseller administrator', reseller_viewer: 'Read-only member', username: 'Username',
        customer_management: 'Customer management', customer_desc: 'Customers do not have separate login accounts. Phase one permits one active key per customer.', new_customer: 'New customer', customer_name: 'Customer name', external_ref: 'External customer reference (optional)', external_ref_help: 'Links this customer to an identifier in your CRM, ERP, or contract system. It must be unique within the reseller and may be left blank.', customer_requires_reseller: 'Create and select a reseller before adding a customer.', customer_discount: 'Customer billing percentage (%)', inherit_discount: 'Inherit reseller default billing percentage', effective_discount: 'Effective billing percentage',
        setup_guide: 'First-time setup', setup_guide_desc: 'No reseller data exists yet. Complete these four steps in order.', setup_step_reseller: '1. Create a reseller and select its quota carrier account', setup_step_member: '2. Add an existing login account as an administrator or read-only member', setup_step_customer: '3. Create a customer under the reseller and configure its billing percentage', setup_step_key: '4. Create a quota-controlled API key for the customer', start_setup: 'Create the first reseller',
        discount_management: 'Billing percentage management', discount_desc: 'A customer billing percentage takes precedence over the reseller default. Historical versions remain immutable.', select_customer: 'Select customer', set_discount: 'Set billing percentage', discount_bps: 'Billing percentage (%)', discount_help: 'Enter the percentage of list price to charge: 100 = list price, 85 = 85% of list price, and 120 = 1.2 times list price. Leave blank to inherit the reseller default.', allowed_range: 'Allowed range', effective_at: 'Effective at', source: 'Source', reason: 'Reason', history: 'Version history',
        key_management: 'Customer API key management', key_desc: 'Issue quota-controlled API keys to customers. The complete key appears only once.', key_rules: 'Quota and key-count rules', finite_key_explanation: 'Quota mode is fixed to quota-controlled (unlimited_quota=false), so new calls stop when the balance reaches zero. Each customer may have one Active or Retiring key. The total keys under the carrier account are also limited by MaxUserTokens. These limits affect new calls only; in-flight tasks still settle.', max_user_tokens_rule: 'Current carrier-account key limit', carrier_token_usage: 'Current carrier-account key count', allocation_warning: 'Key usage has reached 80% of the system limit. Contact the platform administrator to plan capacity.', allocation_blocked: 'New customer key creation is currently blocked', create_key: 'Create customer API key', key_name: 'Key name', token_id: 'Token ID', key_preview: 'Key identifier', remain_quota: 'Remaining quota', used_quota: 'Used quota', group: 'Group', models: 'Model scope', models_help: 'Select from models currently available in the gateway. Leave blank to omit a model allowlist.', expires_at: 'Expires at', initial_quota: 'Initial quota',
        one_time_key: 'One-time API key', one_time_warning: 'Save this key securely now. Reseller Hub cannot show it after this dialog closes and never stores it in browser storage.', saved_key: 'I saved the key securely',
        quota_management: 'Add or subtract quota', quota_desc: 'Manual adjustments change remain_quota only, never used_quota. Override is unavailable.', input_unit: 'Input unit', display_currency: 'Display currency', raw_quota: 'Raw quota', amount: 'Adjustment amount', current_balance: 'Current balance', change_amount: 'Change', after_balance: 'Balance after adjustment', standard_amount: 'Standard amount', discounted_amount: 'Customer settlement amount', conversion_snapshot: 'Conversion snapshot', in_flight_warning: 'In-flight requests and async tasks may reduce the final balance below this preview. New calls stop when the balance is zero or negative.', subtract_guard: 'A manual subtraction cannot make the confirmed execution-time balance negative.', idempotency: 'This operation uses an idempotency key; retries do not apply it twice.',
        quota_ledger: 'Quota ledger', ledger_desc: 'Review the actor, before/after balances, currency, and billing-percentage snapshot for every adjustment.', event_id: 'Event ID', operation: 'Operation', before: 'Before', delta: 'Delta', after: 'After', actor: 'Actor', request_id: 'Request ID', ledger_status: 'Ledger status', snapshot_amount: 'Snapshot amount', current_amount: 'Current recalculation',
        usage_title: 'Usage query', usage_desc: 'Filter standard consumption and customer settlement amounts by customer, key, model, and time.', start_time: 'Start time', end_time: 'End time', model: 'Model', standard_usage: 'Standard consumption', prompt_tokens: 'Input tokens', completion_tokens: 'Output tokens', channel_id: 'Channel ID', log_id: 'Log ID',
        audit_title: 'Audit log', audit_desc: 'Trace reseller, member, customer, billing percentage, key, and quota operations.', action: 'Action', target: 'Target', detail: 'Details', ip: 'IP address',
        funding: 'Add carrier quota', funding_title: 'Add shared carrier quota', funding_note: 'Phase one allows Root to add users.quota only. Subtract and override are unavailable.',
        confirm_danger: 'Confirm high-risk action', confirm_disable_customer: 'Suspending this customer blocks new key calls. In-flight tasks continue to settle.', confirm_disable_key: 'Disabling this key blocks new calls. In-flight tasks continue to settle.', confirm_retire_key: 'A retired key accepts no new calls. It remains retiring while async tasks are unfinished.', confirm_remove_member: 'This account will lose access to the reseller data.', confirm_subtract: 'Verify the customer, amount, and reason. This action is recorded in an immutable adjustment ledger.', confirm_reverse: 'Reversal creates an opposite adjustment for the same amount and references the original event. History is never deleted or overwritten.',
        currency_config: 'Currency configuration', quota_per_unit: 'Quota per unit', exchange_rate: 'Effective rate', discount: 'Billing percentage', page_unavailable: 'Your current role cannot access this page.', login_expired: 'Your session expired. Sign in through the main system again.', membership_required: 'Your main-system session is valid, but this account has no active reseller membership. Ask a super administrator to bind it on the Members page.', reseller_inactive: 'The reseller assigned to this account is inactive. Contact a super administrator.'
      }
    };

    var rootNavigation = ['dashboard','resellers','members','customers','discounts','keys','quota','ledger','usage','audit'];
    var resellerNavigation = ['dashboard','customers','discounts','keys','quota','ledger','usage','audit'];
    var modalSubmit = null;
    var confirmSubmit = null;

    function t(key) { return (messages[state.lang] && messages[state.lang][key]) || messages.en[key] || key; }
    function esc(value) { return String(value == null ? '' : value).replace(/[&<>"']/g, function (c) { return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
    function attr(value) { return esc(value); }
    function pick(obj, keys, fallback) { for (var i=0;i<keys.length;i++) { if (obj && obj[keys[i]] != null) return obj[keys[i]]; } return fallback; }
    function listOf(data, keys) {
      if (Array.isArray(data)) return data;
      for (var i=0;i<keys.length;i++) if (data && Array.isArray(data[keys[i]])) return data[keys[i]];
      if (data && data.data) return listOf(data.data, keys);
      return [];
    }
    function number(value, fallback) { var n=Number(value); return Number.isFinite(n) ? n : (fallback || 0); }
    function integer(value) { return Math.trunc(number(value,0)); }
    function formatNumber(value) { return new Intl.NumberFormat(state.lang === 'zh' ? 'zh-CN' : 'en-US').format(integer(value)); }
    function formatDate(value) {
      if (!value) return '-';
      var v = value;
      if (typeof v === 'number' || /^-?\d+$/.test(String(v))) { v = Number(v); if(v<0)return '-'; if (v < 100000000000) v *= 1000; }
      var d = new Date(v); if (isNaN(d.getTime())) return esc(value);
      return new Intl.DateTimeFormat(state.lang === 'zh' ? 'zh-CN' : 'en-US',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}).format(d);
    }
    function datetimeLocalValue(value) {
      if (!value || Number(value) < 0) return '';
      var v=Number(value); if(v<100000000000)v*=1000; var d=new Date(v); if(isNaN(d.getTime()))return '';
      return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);
    }
    function statusBadge(value) { var s = String(value || 'unknown').toLowerCase(); return '<span class="badge '+attr(s)+'">'+esc(t(s) !== s ? t(s) : value || '-')+'</span>'; }
    function discountText(bps) { if (bps == null || bps === '') return t('inherit_discount'); return (number(bps)/100).toFixed(2)+'%'; }
    function discountPercent(bps) { if (bps == null || bps === '') return ''; return number(bps)/100; }
    function discountBPS(percent) { if (percent == null || percent === '') return null; return Math.round(number(percent)*100); }
    function minDiscountPercent() { return number(pick(pick(state.reference,['limits'],{}),['min_discount_bps'],100),100)/100; }
    function maxDiscountPercent() { return number(pick(pick(state.reference,['limits'],{}),['max_discount_bps'],50000),50000)/100; }
    function discountHelp() { return t('discount_help')+' '+t('allowed_range')+': '+minDiscountPercent()+'%–'+maxDiscountPercent()+'%.'; }
    function conversionRate() {
      var c = state.conversion || {};
      var currency = String(pick(c,['display_type','currency','display_currency','currency_type'],'USD')).toUpperCase();
      if (currency === 'USD') return 1;
      return number(pick(c,['usd_to_display_rate','effective_rate','usd_exchange_rate','exchange_rate'],1),1);
    }
    function currencySymbol() { return pick(state.conversion,['symbol','currency_symbol'],String(pick(state.conversion,['display_type','currency','display_currency'],'USD')).toUpperCase() === 'CNY' ? '¥' : '$'); }
    function quotaPerUnit() { return Math.max(1,number(pick(state.conversion,['quota_per_unit','quotaPerUnit'],500000),500000)); }
    function standardMoney(quota) { return number(quota)/quotaPerUnit()*conversionRate(); }
    function customerMoney(quota, bps) { return standardMoney(quota)*(number(bps,10000)/10000); }
    function money(value) { return currencySymbol()+number(value).toLocaleString(state.lang === 'zh' ? 'zh-CN' : 'en-US',{minimumFractionDigits:2,maximumFractionDigits:6}); }
    function moneyQuota(quota, bps) {
      var q=integer(quota), cls=q<0?' negative':'';
      return '<span class="amount'+cls+'">'+esc(money(customerMoney(q,bps == null ? 10000 : bps)))+'<span class="subvalue">'+formatNumber(q)+' quota</span></span>';
    }
    function requiredLabel(key) { return esc(t(key))+'<span class="required" title="'+esc(t('required'))+'">*</span>'; }
    function uuidv7() {
      var bytes = new Uint8Array(16); crypto.getRandomValues(bytes);
      var now = BigInt(Date.now());
      for (var i=5;i>=0;i--) { bytes[i]=Number(now & 255n); now >>= 8n; }
      bytes[6]=(bytes[6]&15)|112; bytes[8]=(bytes[8]&63)|128;
      var h=Array.from(bytes,function(b){return b.toString(16).padStart(2,'0');}).join('');
      return h.slice(0,8)+'-'+h.slice(8,12)+'-'+h.slice(12,16)+'-'+h.slice(16,20)+'-'+h.slice(20);
    }
    function getCookie(name) { var m=document.cookie.match(new RegExp('(?:^|; )'+name.replace(/[.$?*|{}()[\]\\/+^]/g,'\\$&')+'=([^;]*)')); return m ? decodeURIComponent(m[1]) : ''; }
    function getNewAPIUserId() {
      try {
        var uid=window.localStorage.getItem('uid');
        if (uid) return uid;
        var rawUser=window.localStorage.getItem('user');
        if (rawUser) {
          var user=JSON.parse(rawUser);
          if (user && user.id) return String(user.id);
        }
      } catch (_) {}
      return '';
    }
    function requestHeaders(write, eventId) {
      var headers={'Accept':'application/json'};
      if (write) headers['Content-Type']='application/json';
      var uid=getNewAPIUserId();
      if (uid) headers['New-Api-User']=uid;
      var auth='';
      try { auth=window.localStorage.getItem('new_api_access_token')||''; } catch(_) {}
      if (auth) headers['Authorization']='Bearer '+auth;
      var csrf=state.csrf || getCookie('csrf_token') || getCookie('csrf');
      if (csrf) { headers['X-CSRF-Token']=csrf; headers['X-CSRFToken']=csrf; }
      if (eventId) { headers['Idempotency-Key']=eventId; headers['X-Idempotency-Key']=eventId; }
      return headers;
    }
    async function api(path, options) {
      options=options||{}; var write=!!options.method && options.method!=='GET'; var eventId=options.eventId;
      var response=await fetch(path,{method:options.method||'GET',credentials:'same-origin',headers:Object.assign(requestHeaders(write,eventId),options.headers||{}),body:options.body == null ? undefined : JSON.stringify(options.body)});
      var text=await response.text(), payload=null;
      if (text) { try { payload=JSON.parse(text); } catch (_) { payload={message:text}; } }
      if (response.status===401) throw new Error(t('login_expired'));
      if (response.status===403 && (path===API+'/me'||path===API+'/auth/me')) {
        throw new Error(pick(payload,['message'],'')==='reseller is not active'?t('reseller_inactive'):t('membership_required'));
      }
      if (!response.ok || (payload && payload.success===false)) throw new Error(pick(payload,['message','error'],response.status+' '+response.statusText));
      return payload && Object.prototype.hasOwnProperty.call(payload,'data') ? payload.data : (payload||{});
    }
    function toast(message, type) {
      var el=document.createElement('div'); el.className='toast '+(type||''); el.textContent=message; document.getElementById('toasts').appendChild(el);
      setTimeout(function(){el.remove();},4200);
    }
    function loading() { document.getElementById('content').innerHTML='<div class="loading"><div><div class="spinner"></div><div style="margin-top:8px">'+esc(t('loading'))+'</div></div></div>'; }
    function emptyRow(colspan) { return '<tr><td colspan="'+colspan+'"><div class="empty"><strong>'+esc(t('no_data'))+'</strong>'+esc(t('no_data_hint'))+'</div></td></tr>'; }
    function pageHead(title,desc,actions) { return '<div class="page-head"><div><h1>'+esc(t(title))+'</h1><p>'+esc(t(desc))+'</p></div><div class="actions">'+(actions||'')+'</div></div>'; }
    function field(label,input,wide,help) { return '<div class="field '+(wide?'wide':'')+'"><label>'+label+'</label>'+input+(help?'<div class="help">'+esc(help)+'</div>':'')+'</div>'; }
    function option(value,label,selected) { return '<option value="'+attr(value)+'" '+(String(value)===String(selected)?'selected':'')+'>'+esc(label)+'</option>'; }
    function query(params) { var s=new URLSearchParams(); Object.keys(params||{}).forEach(function(k){if(params[k]!==''&&params[k]!=null)s.set(k,params[k]);}); var out=s.toString(); return out?'?'+out:''; }
    function idOf(item) { return pick(item,['id','ID','reseller_id','customer_id','token_id'],''); }

    function openForm(config) {
      document.getElementById('formModalTitle').textContent=config.title;
      document.getElementById('formModalBody').innerHTML=config.body;
      document.getElementById('submitButton').textContent=config.submitLabel||t('save');
      document.getElementById('cancelButton').textContent=t('cancel');
      modalSubmit=config.onSubmit;
      document.getElementById('formModal').classList.add('open');
      setTimeout(function(){var f=document.querySelector('#formModal input:not([type=hidden]),#formModal select,#formModal textarea');if(f)f.focus();},0);
    }
    function closeForm() { document.getElementById('formModal').classList.remove('open'); modalSubmit=null; document.getElementById('modalForm').reset(); }
    function openConfirm(title,body,onConfirm,label) {
      document.getElementById('confirmTitle').textContent=title; document.getElementById('confirmBody').innerHTML=body;
      document.getElementById('confirmButton').textContent=label||t('confirm'); document.getElementById('confirmCancel').textContent=t('cancel');
      confirmSubmit=onConfirm; document.getElementById('confirmModal').classList.add('open');
    }
    function closeConfirm() { document.getElementById('confirmModal').classList.remove('open'); confirmSubmit=null; }
    function showSecret(value) {
      document.getElementById('secretTitle').textContent=t('one_time_key'); document.getElementById('secretWarning').textContent=t('one_time_warning');
      document.getElementById('secretLabel').textContent=t('one_time_key'); document.getElementById('secretValue').value=value;
      document.getElementById('copySecret').textContent=t('copy'); document.getElementById('closeSecret').textContent=t('saved_key');
      document.getElementById('secretModal').classList.add('open');
    }
    function closeSecret() { var input=document.getElementById('secretValue'); input.value=''; document.getElementById('secretModal').classList.remove('open'); }
    async function execute(button,fn) { var old=button&&button.textContent; if(button){button.disabled=true;button.textContent='…';} try { var result=await fn(); return result; } catch(err){toast(err.message||t('request_failed'),'error'); throw err;} finally {if(button){button.disabled=false;button.textContent=old;}} }

    function roleName() { return state.isRoot?t('role_root'):(pick(state.me,['hub_role','role_name'],'')==='reseller_viewer'?t('role_viewer'):t('role_reseller')); }
    function renderChrome() {
      var items=state.isRoot?rootNavigation:resellerNavigation;
      document.getElementById('navigation').innerHTML='<div class="nav-caption">'+esc(t('nav_workspace'))+'</div>'+items.map(function(page){return '<button type="button" class="nav-button '+(state.page===page?'active':'')+'" data-page="'+page+'"><span class="nav-icon">'+page.slice(0,2).toUpperCase()+'</span><span>'+esc(t(page))+'</span></button>';}).join('');
      document.getElementById('roleLabel').textContent=roleName(); document.getElementById('identityRole').textContent=roleName();
      document.getElementById('identityName').textContent=pick(state.me,['username','display_name','name'],'-');
      document.getElementById('breadcrumb').textContent='Reseller Hub / '+t(state.page);
      document.querySelectorAll('[data-language]').forEach(function(el){el.classList.toggle('active',el.dataset.language===state.lang);});
      document.documentElement.lang=state.lang==='zh'?'zh-CN':'en';
      document.getElementById('refreshButton').title=t('refresh');
    }
    function navigate(page) {
      var allowed=state.isRoot?rootNavigation:resellerNavigation; if(allowed.indexOf(page)<0) page='dashboard';
      state.page=page; history.replaceState(null,'','#'+page); document.body.classList.remove('menu-open'); renderChrome(); renderPage();
    }
    async function loadResellers() { if(!state.isRoot)return []; var data=await api(API+'/resellers'); state.resellers=listOf(data,['items','resellers','records']); if(!state.selectedResellerId&&state.resellers[0])state.selectedResellerId=idOf(state.resellers[0]); return state.resellers; }
    async function loadCustomers(params) { var data=await api(API+'/customers'+query(params||{})); state.customers=listOf(data,['items','customers','records']); if(!state.selectedCustomerId&&state.customers[0])state.selectedCustomerId=idOf(state.customers[0]); return state.customers; }
    function resellerOptions(selected) { return '<option value="">'+esc(t('select_reseller'))+'</option>'+state.resellers.map(function(r){return option(idOf(r),pick(r,['name','display_name','code'],idOf(r))+' (#'+idOf(r)+')',selected);}).join(''); }
    function customerOptions(selected) { return '<option value="">'+esc(t('select_customer'))+'</option>'+state.customers.map(function(c){return option(idOf(c),pick(c,['display_name','name'],idOf(c))+' (#'+idOf(c)+')',selected);}).join(''); }
    function userOptionList(selected,predicate) { return '<option value="">'+esc(t('select_user'))+'</option>'+listOf(state.reference,['users']).filter(function(u){return number(pick(u,['status'],1),1)===1&&predicate(u);}).map(function(u){var id=pick(u,['id'],'');return option(id,pick(u,['display_name','username'],'-')+' / '+pick(u,['username'],'-')+' (#'+id+')',selected);}).join(''); }
    function carrierUserOptions(selected) { return userOptionList(selected,function(u){return number(pick(u,['role'],0),0)===1;}); }
    function memberUserOptions(selected) { return userOptionList(selected,function(u){return number(pick(u,['role'],0),0)<100;}); }
    function groupOptions(selected) { var groups=listOf(state.reference,['groups']);return '<option value="">'+esc(t('select_group'))+'</option>'+groups.map(function(g){return option(g,g,selected);}).join(''); }
    function modelOptions(selected) { var chosen=Array.isArray(selected)?selected:[];return listOf(state.reference,['models']).map(function(m){return option(m,m,chosen.indexOf(m)>=0);}).join(''); }
    function selectedCustomer() { return state.customers.find(function(c){return String(idOf(c))===String(state.selectedCustomerId);})||{}; }

    async function renderPage() {
      loading();
      try {
        if(state.page==='dashboard')await renderDashboard();
        else if(state.page==='resellers')await renderResellers();
        else if(state.page==='members')await renderMembers();
        else if(state.page==='customers')await renderCustomers();
        else if(state.page==='discounts')await renderDiscounts();
        else if(state.page==='keys')await renderKeys();
        else if(state.page==='quota')await renderQuota();
        else if(state.page==='ledger')await renderLedger();
        else if(state.page==='usage')await renderUsage();
        else if(state.page==='audit')await renderAudit();
      } catch(err) { document.getElementById('content').innerHTML='<div class="notice danger">'+esc(err.message||t('request_failed'))+'</div>'; }
    }

    async function renderDashboard() {
      var results=await Promise.all([state.isRoot?loadResellers():Promise.resolve([]),loadCustomers({page:1,page_size:10}),api(API+'/dashboard-summary')]);
      var customers=results[1],summary=pick(results[2],['summary'],{}),alerts=listOf(results[2],['alerts']),activeKeys=number(pick(summary,['active_key_count'],0)),negative=number(pick(summary,['negative_key_count'],0)),managed=number(pick(summary,['managed_balance'],0)),carrier=number(pick(summary,['carrier_balance'],0)),customerCount=number(pick(summary,['customer_count'],0)),resellerCount=number(pick(results[2],['reseller_count'],state.resellers.length));
      var metrics=(state.isRoot?[
        [t('reseller_count'),resellerCount,''],[t('customer_count'),customerCount,''],[t('active_key_count'),activeKeys,''],[t('negative_count'),negative,'']
      ]:[
        [t('customer_count'),customerCount,''],[t('carrier_balance'),moneyQuota(carrier,10000),''],[t('managed_balance'),moneyQuota(managed,10000),''],[t('negative_count'),negative,'']
      ]).map(function(m){return '<div class="metric"><div class="metric-label">'+esc(m[0])+'</div><div class="metric-value">'+m[1]+'</div>'+(m[2]?'<div class="metric-note">'+esc(m[2])+'</div>':'')+'</div>';}).join('');
      var alertRows=alerts.map(function(a){var code=pick(a,['code'],'unknown'),value=pick(a,['value'],''),limit=pick(a,['limit'],''),name=pick(a,['reseller_name'],''),tokenId=pick(a,['token_id'],'');return '<div class="notice '+(pick(a,['severity'],'warning')==='danger'?'danger':'')+'"><strong>'+esc(t('alert_'+code))+'</strong><div style="margin-top:4px">'+esc((name?name+' · ':'')+(tokenId?'Token #'+tokenId+' · ':'')+(value!==''?value:'')+(limit?(' / '+limit):''))+'</div></div>';}).join('')||'<div class="notice info">'+esc(t('no_alerts'))+'</div>';
      var rows=customers.map(function(c){var q=pick(c,['remain_quota','token_remain_quota'],0),bps=pick(c,['effective_discount_bps','discount_bps'],10000);return '<tr><td>'+esc(pick(c,['display_name','name'],'-'))+'</td><td class="mono">'+esc(pick(c,['external_ref'],'-'))+'</td><td>'+statusBadge(pick(c,['status'],'active'))+'</td><td>'+discountText(bps)+'</td><td>'+moneyQuota(q,bps)+'</td><td>'+formatDate(pick(c,['updated_at','created_at'],''))+'</td></tr>';}).join('')||emptyRow(6);
      var setup=state.isRoot&&resellerCount===0?'<section class="panel"><div class="panel-head"><h2>'+esc(t('setup_guide'))+'</h2></div><div class="panel-body"><div class="notice info"><strong>'+esc(t('setup_guide_desc'))+'</strong><div style="margin-top:10px;line-height:1.9">'+esc(t('setup_step_reseller'))+'<br>'+esc(t('setup_step_member'))+'<br>'+esc(t('setup_step_customer'))+'<br>'+esc(t('setup_step_key'))+'</div><div class="actions" style="margin-top:12px"><button class="button primary" id="startSetup">'+esc(t('start_setup'))+'</button></div></div></div></section>':'';
      document.getElementById('content').innerHTML=pageHead('dashboard_title',state.isRoot?'dashboard_desc_root':'dashboard_desc_reseller','')+'<div class="grid metrics">'+metrics+'</div>'+setup+'<section class="panel"><div class="panel-head"><h2>'+esc(t('operational_alerts'))+'</h2></div><div class="panel-body">'+alertRows+'</div></section><section class="panel"><div class="panel-head"><h2>'+esc(t('recent_activity'))+'</h2></div><div class="table-shell"><table><thead><tr><th>'+esc(t('customer_name'))+'</th><th>'+esc(t('external_ref'))+'</th><th>'+esc(t('status'))+'</th><th>'+esc(t('effective_discount'))+'</th><th>'+esc(t('remain_quota'))+'</th><th>'+esc(t('updated_at'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      var start=document.getElementById('startSetup');if(start)start.onclick=function(){navigate('resellers');};
    }

    async function renderResellers() {
      await loadResellers();
      var rows=state.resellers.map(function(r){var id=idOf(r),q=pick(r,['carrier_quota','quota_carrier_balance'],0),bps=pick(r,['default_discount_bps'],10000);return '<tr><td>'+esc(pick(r,['name','display_name'],'-'))+'<span class="subvalue mono">'+esc(pick(r,['code'],'#'+id))+'</span></td><td>'+statusBadge(pick(r,['status'],'active'))+'</td><td>'+discountText(bps)+'</td><td>'+esc(pick(r,['quota_carrier_username','carrier_username'],'#'+pick(r,['quota_carrier_user_id'],'-')))+'</td><td>'+moneyQuota(q,10000)+'</td><td>'+formatNumber(pick(r,['customer_count'],0))+'</td><td>'+formatDate(pick(r,['created_at'],''))+'</td><td><div class="row-actions"><button class="button quiet compact" data-edit-reseller="'+attr(id)+'">'+esc(t('edit'))+'</button><button class="button quiet compact" data-fund-reseller="'+attr(id)+'">'+esc(t('funding'))+'</button></div></td></tr>';}).join('')||emptyRow(8);
      document.getElementById('content').innerHTML=pageHead('reseller_management','reseller_desc','<button class="button primary" id="newReseller">+ '+esc(t('new_reseller'))+'</button>')+'<section class="panel"><div class="table-shell"><table><thead><tr><th>'+esc(t('reseller_name'))+'</th><th>'+esc(t('status'))+'</th><th>'+esc(t('default_discount'))+'</th><th>'+esc(t('carrier_user'))+'</th><th>'+esc(t('carrier_balance'))+'</th><th>'+esc(t('customer_count'))+'</th><th>'+esc(t('created_at'))+'</th><th>'+esc(t('actions'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      document.getElementById('newReseller').onclick=function(){resellerForm(null);};
      document.querySelectorAll('[data-edit-reseller]').forEach(function(b){b.onclick=function(){resellerForm(state.resellers.find(function(r){return String(idOf(r))===b.dataset.editReseller;}));};});
      document.querySelectorAll('[data-fund-reseller]').forEach(function(b){b.onclick=function(){fundingForm(b.dataset.fundReseller);};});
    }
    function resellerForm(item) {
      item=item||{}; var editing=!!idOf(item);
      var codeField=editing?field(t('reseller_code'),'<input name="code" value="'+attr(pick(item,['code'],''))+'" readonly>',false,t('reseller_code_hint')):field(t('reseller_code'),'<div class="notice info">'+esc(t('reseller_code_hint'))+'</div>');
      var body='<div class="form-grid">'+
        field(requiredLabel('reseller_name'),'<input name="name" required maxlength="120" value="'+attr(pick(item,['name','display_name'],''))+'">')+
        codeField+
        field(requiredLabel('carrier_user'),'<select name="quota_carrier_user_id" required>'+carrierUserOptions(pick(item,['quota_carrier_user_id'],''))+'</select>',false,t('carrier_user_hint'))+
        field(requiredLabel('default_discount'),'<input name="default_discount_percent" type="number" min="'+attr(minDiscountPercent())+'" max="'+attr(maxDiscountPercent())+'" step="0.01" required value="'+attr(discountPercent(pick(item,['default_discount_bps'],10000)))+'">',false,discountHelp())+
        field(requiredLabel('status'),'<select name="status" required>'+option('active',t('active'),pick(item,['status'],'active'))+option('suspended',t('suspended'),pick(item,['status'],''))+option('closed',t('closed'),pick(item,['status'],''))+'</select>')+'</div>';
      openForm({title:editing?t('edit')+' '+t('reseller_name'):t('new_reseller'),body:body,onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries());data.quota_carrier_user_id=integer(data.quota_carrier_user_id);data.default_discount_bps=discountBPS(data.default_discount_percent);delete data.default_discount_percent;await execute(button,function(){return api(API+'/resellers'+(editing?'/'+idOf(item):''),{method:editing?'PATCH':'POST',body:data,eventId:uuidv7()});});closeForm();toast(t('operation_success'),'success');renderResellers();}});
    }
    function fundingForm(resellerId) {
      var body='<div class="notice info">'+esc(t('funding_note'))+'</div><div class="form-grid">'+field(requiredLabel('input_unit'),'<select name="input_unit" required>'+option('display_currency',t('display_currency'),'display_currency')+option('quota',t('raw_quota'),'')+'</select>')+field(requiredLabel('amount'),'<input name="amount" type="number" min="0.000001" step="0.000001" required>')+field(requiredLabel('reason'),'<textarea name="reason" required maxlength="500"></textarea>',true)+'</div>';
      openForm({title:t('funding_title'),body:body,submitLabel:t('add'),onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries()),eventId=uuidv7();data.mode='add';data.idempotency_key=eventId;await execute(button,function(){return api(API+'/resellers/'+resellerId+'/funding-adjustments',{method:'POST',body:data,eventId:eventId});});closeForm();toast(t('operation_success'),'success');renderResellers();}});
    }

    async function renderMembers() {
      await loadResellers(); var resellerId=state.selectedResellerId, members=[];
      if(resellerId){var detail=await api(API+'/resellers/'+resellerId);members=listOf(detail,['members','items']);}
      var rows=members.map(function(m){var id=pick(m,['id','member_id'],'');return '<tr><td>'+esc(pick(m,['username','user_name'],'#'+pick(m,['new_api_user_id','user_id'],'-')))+'</td><td class="mono">'+esc(pick(m,['new_api_user_id','user_id'],'-'))+'</td><td>'+statusBadge(pick(m,['role'],'reseller_viewer'))+'</td><td>'+statusBadge(pick(m,['status'],'active'))+'</td><td>'+formatDate(pick(m,['created_at'],''))+'</td><td><button class="button danger compact" data-remove-member="'+attr(id)+'">'+esc(t('remove'))+'</button></td></tr>';}).join('')||emptyRow(6);
      var prerequisite=!resellerId?'<div class="notice info" style="margin:12px">'+esc(t('member_requires_reseller'))+'</div>':'';
      document.getElementById('content').innerHTML=pageHead('member_management','member_desc','<button class="button primary" id="addMember" '+(!resellerId?'disabled title="'+attr(t('member_requires_reseller'))+'"':'')+'>+ '+esc(t('add_member'))+'</button>')+'<section class="panel">'+prerequisite+'<div class="filterbar"><div class="field"><label>'+esc(t('select_reseller'))+'</label><select id="memberReseller">'+resellerOptions(resellerId)+'</select></div></div><div class="table-shell"><table><thead><tr><th>'+esc(t('username'))+'</th><th>'+esc(t('user_id'))+'</th><th>'+esc(t('member_role'))+'</th><th>'+esc(t('status'))+'</th><th>'+esc(t('created_at'))+'</th><th>'+esc(t('actions'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      document.getElementById('memberReseller').onchange=function(e){state.selectedResellerId=e.target.value;renderMembers();};
      document.getElementById('addMember').onclick=function(){memberForm(resellerId);};
      document.querySelectorAll('[data-remove-member]').forEach(function(b){b.onclick=function(){openConfirm(t('confirm_danger'),'<div class="notice danger">'+esc(t('confirm_remove_member'))+'</div>',async function(btn){await execute(btn,function(){return api(API+'/resellers/'+resellerId+'/members/'+b.dataset.removeMember,{method:'DELETE',body:{},eventId:uuidv7()});});closeConfirm();toast(t('operation_success'),'success');renderMembers();},t('remove'));};});
    }
    function memberForm(resellerId) {
      var body='<div class="form-grid">'+field(requiredLabel('user_id'),'<select name="new_api_user_id" required>'+memberUserOptions('')+'</select>')+field(requiredLabel('member_role'),'<select name="role" required>'+option('reseller_admin',t('reseller_admin'),'')+option('reseller_viewer',t('reseller_viewer'),'')+'</select>')+'</div>';
      openForm({title:t('add_member'),body:body,onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries());data.new_api_user_id=integer(data.new_api_user_id);data.status='active';await execute(button,function(){return api(API+'/resellers/'+resellerId+'/members',{method:'POST',body:data,eventId:uuidv7()});});closeForm();toast(t('operation_success'),'success');renderMembers();}});
    }

    async function renderCustomers() {
      var q=pick(window.__customerFilters,['q'],'')||'',status=pick(window.__customerFilters,['status'],'')||'';
      if(state.isRoot)await loadResellers(); await loadCustomers({q:q,status:status,reseller_id:state.isRoot?state.selectedResellerId:''});
      var rows=state.customers.map(function(c){var id=idOf(c),bps=pick(c,['effective_discount_bps','discount_bps'],10000),qv=pick(c,['remain_quota','token_remain_quota'],0),actions='';if(state.canWrite){actions='<button class="button quiet compact" data-edit-customer="'+attr(id)+'">'+esc(t('edit'))+'</button>'+(String(pick(c,['status'],'active'))==='active'?'<button class="button danger compact" data-disable-customer="'+attr(id)+'">'+esc(t('disable'))+'</button>':'<button class="button compact" data-enable-customer="'+attr(id)+'">'+esc(t('enable'))+'</button>');}return '<tr><td>'+esc(pick(c,['display_name','name'],'-'))+'<span class="subvalue mono">#'+esc(id)+'</span></td><td class="mono">'+esc(pick(c,['external_ref'],'-'))+'</td>'+(state.isRoot?'<td>'+esc(pick(c,['reseller_name'],'#'+pick(c,['reseller_id'],'-')))+'</td>':'')+'<td>'+statusBadge(pick(c,['status'],'active'))+'</td><td>'+discountText(bps)+'</td><td>'+moneyQuota(qv,bps)+'</td><td>'+formatDate(pick(c,['updated_at','created_at'],''))+'</td><td><div class="row-actions">'+actions+'</div></td></tr>';}).join('')||emptyRow(state.isRoot?8:7);
      var rootFilter=state.isRoot?'<div class="field"><label>'+esc(t('select_reseller'))+'</label><select id="customerReseller">'+resellerOptions(state.selectedResellerId)+'</select></div>':'';
      var hasReseller=!state.isRoot||!!state.selectedResellerId,canCreate=state.canWrite&&hasReseller,prerequisite=state.isRoot&&!hasReseller?'<div class="notice info" style="margin:12px">'+esc(t('customer_requires_reseller'))+'</div>':'',createAction=state.canWrite?'<button class="button primary" id="newCustomer" '+(!hasReseller?'disabled title="'+attr(t('customer_requires_reseller'))+'"':'')+'>+ '+esc(t('new_customer'))+'</button>':'';
      document.getElementById('content').innerHTML=pageHead('customer_management','customer_desc',createAction)+'<section class="panel">'+prerequisite+'<div class="filterbar">'+rootFilter+'<div class="field grow"><label>'+esc(t('search'))+'</label><input id="customerSearch" value="'+attr(q)+'" placeholder="'+attr(t('customer_name')+' / '+t('external_ref'))+'"></div><div class="field"><label>'+esc(t('status'))+'</label><select id="customerStatus">'+option('',t('all'),status)+option('active',t('active'),status)+option('suspended',t('suspended'),status)+option('closed',t('closed'),status)+'</select></div><button class="button" id="applyCustomerFilters">'+esc(t('apply'))+'</button></div><div class="table-shell"><table><thead><tr><th>'+esc(t('customer_name'))+'</th><th>'+esc(t('external_ref'))+'</th>'+(state.isRoot?'<th>'+esc(t('reseller_name'))+'</th>':'')+'<th>'+esc(t('status'))+'</th><th>'+esc(t('effective_discount'))+'</th><th>'+esc(t('remain_quota'))+'</th><th>'+esc(t('updated_at'))+'</th><th>'+esc(t('actions'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      var newCustomer=document.getElementById('newCustomer');if(newCustomer&&canCreate)newCustomer.onclick=function(){customerForm(null);};
      if(state.isRoot)document.getElementById('customerReseller').onchange=function(e){state.selectedResellerId=e.target.value;renderCustomers();};
      document.getElementById('applyCustomerFilters').onclick=function(){window.__customerFilters={q:document.getElementById('customerSearch').value,status:document.getElementById('customerStatus').value};renderCustomers();};
      document.querySelectorAll('[data-edit-customer]').forEach(function(b){b.onclick=function(){customerForm(state.customers.find(function(c){return String(idOf(c))===b.dataset.editCustomer;}));};});
      document.querySelectorAll('[data-disable-customer]').forEach(function(b){b.onclick=function(){customerStatusChange(b.dataset.disableCustomer,'suspended',true);};});
      document.querySelectorAll('[data-enable-customer]').forEach(function(b){b.onclick=function(){customerStatusChange(b.dataset.enableCustomer,'active',false);};});
    }
    function customerForm(item) {
      item=item||{};var editing=!!idOf(item);
      var resellerField=state.isRoot?field(requiredLabel('select_reseller'),'<select name="reseller_id" required>'+resellerOptions(pick(item,['reseller_id'],state.selectedResellerId))+'</select>'):'';
      var discountField=editing?'':field(t('customer_discount'),'<input name="discount_percent" type="number" min="'+attr(minDiscountPercent())+'" max="'+attr(maxDiscountPercent())+'" step="0.01" placeholder="'+attr(t('inherit_discount'))+'">',false,discountHelp());
      var body='<div class="form-grid">'+resellerField+field(requiredLabel('customer_name'),'<input name="display_name" required maxlength="120" value="'+attr(pick(item,['display_name','name'],''))+'">')+field(t('external_ref'),'<input name="external_ref" maxlength="120" value="'+attr(pick(item,['external_ref'],''))+'">',false,t('external_ref_help'))+discountField+field(requiredLabel('status'),'<select name="status" required>'+option('active',t('active'),pick(item,['status'],'active'))+option('suspended',t('suspended'),pick(item,['status'],''))+option('closed',t('closed'),pick(item,['status'],''))+'</select>')+'</div>';
      openForm({title:editing?t('edit')+' '+t('customer_name'):t('new_customer'),body:body,onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries());if(data.reseller_id)data.reseller_id=integer(data.reseller_id);if(!editing)data.discount_bps=discountBPS(data.discount_percent);delete data.discount_percent;await execute(button,function(){return api(API+'/customers'+(editing?'/'+idOf(item):''),{method:editing?'PATCH':'POST',body:data,eventId:uuidv7()});});closeForm();toast(t('operation_success'),'success');renderCustomers();}});
    }
    function customerStatusChange(id,status,danger) {
      var run=async function(btn){await execute(btn,function(){return api(API+'/customers/'+id,{method:'PATCH',body:{status:status},eventId:uuidv7()});});closeConfirm();toast(t('operation_success'),'success');renderCustomers();};
      if(danger)openConfirm(t('confirm_danger'),'<div class="notice danger">'+esc(t('confirm_disable_customer'))+'</div>',run,t('disable'));else run(null);
    }

    async function prepareCustomerPage() { if(state.isRoot)await loadResellers(); await loadCustomers({reseller_id:state.isRoot?state.selectedResellerId:''}); }
    function customerPicker(id) { return '<div class="field"><label>'+esc(t('select_customer'))+'</label><select id="pageCustomer">'+customerOptions(id)+'</select></div>'; }
    function bindCustomerPicker(renderer) { var el=document.getElementById('pageCustomer');if(el)el.onchange=function(e){state.selectedCustomerId=e.target.value;renderer();}; }

    async function renderDiscounts() {
      await prepareCustomerPage(); var customerId=state.selectedCustomerId, detail={},history=[];
      if(customerId){detail=await api(API+'/customers/'+customerId);history=listOf(detail,['discount_history','discounts','versions']);}
      var current=pick(detail,['effective_discount_bps','discount_bps'],pick(selectedCustomer(),['effective_discount_bps','discount_bps'],null));
      var rows=history.map(function(v){return '<tr><td>'+discountText(pick(v,['discount_bps'],null))+'</td><td>'+esc(pick(v,['source'],'customer'))+'</td><td>'+formatDate(pick(v,['effective_at','created_at'],''))+'</td><td class="wrap">'+esc(pick(v,['reason'],'-'))+'</td><td>'+esc(pick(v,['actor_name','created_by_user_id'],'-'))+'</td></tr>';}).join('')||emptyRow(5);
      document.getElementById('content').innerHTML=pageHead('discount_management','discount_desc',state.canWrite&&customerId?'<button class="button primary" id="setDiscount">'+esc(t('set_discount'))+'</button>':'')+'<section class="panel"><div class="filterbar">'+customerPicker(customerId)+'</div>'+(customerId?'<div class="panel-body"><div class="metric" style="max-width:300px"><div class="metric-label">'+esc(t('effective_discount'))+'</div><div class="metric-value">'+esc(discountText(current))+'</div></div></div>':'')+'<div class="panel-head"><h2>'+esc(t('history'))+'</h2></div><div class="table-shell"><table><thead><tr><th>'+esc(t('discount'))+'</th><th>'+esc(t('source'))+'</th><th>'+esc(t('effective_at'))+'</th><th>'+esc(t('reason'))+'</th><th>'+esc(t('actor'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      bindCustomerPicker(renderDiscounts); var b=document.getElementById('setDiscount');if(b)b.onclick=function(){discountForm(customerId);};
    }
    function discountForm(customerId) {
      var body='<div class="form-grid">'+field(t('discount_bps'),'<input name="discount_percent" type="number" min="'+attr(minDiscountPercent())+'" max="'+attr(maxDiscountPercent())+'" step="0.01" placeholder="'+attr(t('inherit_discount'))+'">',false,discountHelp())+field(requiredLabel('reason'),'<textarea name="reason" required maxlength="500"></textarea>',true)+'</div>';
      openForm({title:t('set_discount'),body:body,onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries());data.discount_bps=discountBPS(data.discount_percent);delete data.discount_percent;await execute(button,function(){return api(API+'/customers/'+customerId+'/discounts',{method:'POST',body:data,eventId:uuidv7()});});closeForm();toast(t('operation_success'),'success');renderDiscounts();}});
    }

    async function renderKeys() {
      await prepareCustomerPage(); var customerId=state.selectedCustomerId,tokens=[],keyLimits=pick(state.reference,['limits'],{});
      if(customerId){var data=await api(API+'/customers/'+customerId+'/tokens');tokens=listOf(data,['tokens','items','records']);keyLimits=Object.assign({},keyLimits,pick(data,['limits'],{}));}
      var rows=tokens.map(function(k){var id=pick(k,['new_api_token_id','token_id','id'],''),keyHint=pick(k,['key_prefix','key_preview','key_hint'],'••••'),fingerprint=pick(k,['fingerprint'],''),status=String(pick(k,['status'],'active')),mappingActive=String(pick(k,['mapping_status'],'active'))==='active',actions='';if(mappingActive&&state.canWrite){actions='<button class="button quiet compact" data-edit-key="'+attr(id)+'">'+esc(t('edit'))+'</button>'+(status==='active'?'<button class="button danger compact" data-disable-key="'+attr(id)+'">'+esc(t('disable'))+'</button>':'<button class="button compact" data-enable-key="'+attr(id)+'">'+esc(t('enable'))+'</button>')+'<button class="button danger compact" data-retire-key="'+attr(id)+'">'+esc(t('retire'))+'</button>';}return '<tr><td>'+esc(pick(k,['name'],'-'))+'<span class="subvalue mono">#'+esc(id)+'</span></td><td class="mono">'+esc(keyHint)+(fingerprint?'<span class="subvalue">'+esc(fingerprint)+'</span>':'')+'</td><td>'+statusBadge(status)+'</td><td>'+moneyQuota(pick(k,['remain_quota'],0),pick(selectedCustomer(),['effective_discount_bps','discount_bps'],10000))+'</td><td>'+formatNumber(pick(k,['used_quota'],0))+'</td><td>'+esc(pick(k,['group'],'-'))+'</td><td class="wrap">'+esc(pick(k,['models','model_limits'],'-'))+'</td><td>'+formatDate(pick(k,['expired_time','expires_at'],''))+'</td><td><div class="row-actions">'+actions+'</div></td></tr>';}).join('')||emptyRow(9);
      var maxKeys=pick(keyLimits,['max_user_tokens'],0),usedKeys=pick(keyLimits,['carrier_token_count'],null),blockReason=pick(keyLimits,['allocation_block_reason'],''),allocationWarning=pick(keyLimits,['allocation_warning'],''),limitText=t('finite_key_explanation')+(maxKeys?' '+t('max_user_tokens_rule')+': '+maxKeys+'.':'')+(usedKeys!=null?' '+t('carrier_token_usage')+': '+usedKeys+(maxKeys?' / '+maxKeys:'')+'.':'');
      document.getElementById('content').innerHTML=pageHead('key_management','key_desc',state.canWrite&&customerId?'<button class="button primary" id="createKey" '+(blockReason?'disabled':'')+'>+ '+esc(t('create_key'))+'</button>':'')+'<section class="panel"><div class="filterbar">'+customerPicker(customerId)+'</div><div class="notice info" style="margin:12px"><strong>'+esc(t('key_rules'))+'</strong><div style="margin-top:5px">'+esc(limitText)+'</div></div>'+(allocationWarning?'<div class="notice warning" style="margin:12px">'+esc(t('allocation_warning'))+'</div>':'')+(blockReason?'<div class="notice danger" style="margin:12px"><strong>'+esc(t('allocation_blocked'))+'</strong><div style="margin-top:5px">'+esc(blockReason)+'</div></div>':'')+'<div class="notice info" style="margin:12px">'+esc(t('one_time_warning'))+'</div><div class="table-shell"><table><thead><tr><th>'+esc(t('key_name'))+'</th><th>'+esc(t('key_preview'))+'</th><th>'+esc(t('status'))+'</th><th>'+esc(t('remain_quota'))+'</th><th>'+esc(t('used_quota'))+'</th><th>'+esc(t('group'))+'</th><th>'+esc(t('models'))+'</th><th>'+esc(t('expires_at'))+'</th><th>'+esc(t('actions'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      bindCustomerPicker(renderKeys);var create=document.getElementById('createKey');if(create)create.onclick=function(){keyForm(customerId);};
      document.querySelectorAll('[data-edit-key]').forEach(function(b){b.onclick=function(){keyForm(customerId,tokens.find(function(k){return String(pick(k,['new_api_token_id','token_id','id'],''))===b.dataset.editKey;}));};});
      document.querySelectorAll('[data-disable-key]').forEach(function(b){b.onclick=function(){keyStatus(customerId,b.dataset.disableKey,'disabled',true);};});
      document.querySelectorAll('[data-enable-key]').forEach(function(b){b.onclick=function(){keyStatus(customerId,b.dataset.enableKey,'active',false);};});
      document.querySelectorAll('[data-retire-key]').forEach(function(b){b.onclick=function(){openConfirm(t('confirm_danger'),'<div class="notice danger">'+esc(t('confirm_retire_key'))+'</div>',async function(btn){await execute(btn,function(){return api(API+'/customers/'+customerId+'/tokens/'+b.dataset.retireKey+'/retire',{method:'POST',body:{reason:'manual retirement'},eventId:uuidv7()});});closeConfirm();toast(t('operation_success'),'success');renderKeys();},t('retire'));};});
    }
    function keyForm(customerId,item) {
      item=item||{};var tokenId=pick(item,['new_api_token_id','token_id','id'],''),editing=!!tokenId,selectedModels=pick(item,['models'],[]);
      var notice=editing?'':'<div class="notice info">'+esc(t('one_time_warning'))+'</div>';
      var body=notice+'<div class="form-grid">'+field(requiredLabel('key_name'),'<input name="name" required maxlength="64" value="'+attr(pick(item,['name'],''))+'">')+field(requiredLabel('group'),'<select name="group" required>'+groupOptions(pick(item,['group'],''))+'</select>')+field(t('expires_at'),'<input name="expires_at" type="datetime-local" value="'+attr(datetimeLocalValue(pick(item,['expired_time','expires_at'],'')))+'">')+field(t('models'),'<select name="models" multiple size="8">'+modelOptions(selectedModels)+'</select>',true,t('models_help'))+'</div>';
      openForm({title:editing?t('edit')+' '+t('key_name'):t('create_key'),body:body,onSubmit:async function(form,button){var data=Object.fromEntries(new FormData(form).entries());data.models=Array.from(form.elements.models.selectedOptions).map(function(v){return v.value;});if(!data.expires_at)delete data.expires_at;var result=await execute(button,function(){return api(API+'/customers/'+customerId+'/tokens'+(editing?'/'+tokenId:''),{method:editing?'PATCH':'POST',body:data,eventId:uuidv7()});});closeForm();var secret=pick(result,['api_key','key','token','secret'],'');if(secret)showSecret(secret);else toast(t('operation_success'),'success');renderKeys();}});
    }
    function keyStatus(customerId,tokenId,status,danger) {
      var run=async function(btn){await execute(btn,function(){return api(API+'/customers/'+customerId+'/tokens/'+tokenId,{method:'PATCH',body:{status:status},eventId:uuidv7()});});closeConfirm();toast(t('operation_success'),'success');renderKeys();};
      if(danger)openConfirm(t('confirm_danger'),'<div class="notice danger">'+esc(t('confirm_disable_key'))+'</div>',run,t('disable'));else run(null);
    }

    async function renderQuota() {
      await prepareCustomerPage(); var customerId=state.selectedCustomerId,tokens=[],ledger=[];
      if(customerId){var results=await Promise.all([api(API+'/customers/'+customerId+'/tokens'),api(API+'/customers/'+customerId+'/quota-ledger'+query({page:1,page_size:8}))]);tokens=listOf(results[0],['tokens','items']);ledger=listOf(results[1],['items','ledger','records']);}
      var token=tokens.find(function(k){return String(pick(k,['status'],'active'))==='active';})||tokens[0]||{};var current=number(pick(token,['remain_quota'],pick(selectedCustomer(),['remain_quota','token_remain_quota'],0))),bps=number(pick(selectedCustomer(),['effective_discount_bps','discount_bps'],10000),10000);
      var recent=ledger.map(function(e){return '<tr><td class="mono">'+esc(pick(e,['event_id'],'-'))+'</td><td>'+statusBadge(pick(e,['operation','mode'],'-'))+'</td><td>'+moneyQuota(pick(e,['quota_delta','signed_quota_delta','delta_quota','requested_quota'],0),bps)+'</td><td>'+statusBadge(pick(e,['status'],'-'))+'</td><td>'+formatDate(pick(e,['created_at'],''))+'</td></tr>';}).join('')||emptyRow(5);
      var form=customerId?(state.canWrite?'<div class="panel-body"><div class="notice info">'+esc(t('in_flight_warning'))+'</div><form id="quotaForm"><div class="form-grid">'+field(requiredLabel('operation'),'<select name="mode" id="quotaMode" required>'+option('add',t('add'),'add')+option('subtract',t('subtract'),'')+'</select>')+field(requiredLabel('input_unit'),'<select name="input_unit" id="quotaUnit" required>'+option('display_currency',t('display_currency'),'display_currency')+option('quota',t('raw_quota'),'')+'</select>')+field(requiredLabel('amount'),'<input name="amount" id="quotaAmount" type="number" min="0.000001" step="0.000001" required>')+field(requiredLabel('reason'),'<textarea name="reason" required maxlength="500"></textarea>',true)+'</div><div id="quotaPreview" style="margin-top:14px"></div><div class="actions" style="justify-content:flex-end;margin-top:14px"><button class="button primary" type="submit">'+esc(t('confirm'))+'</button></div></form></div>':'<div class="empty">'+esc(t('page_unavailable'))+'</div>'):'<div class="empty">'+esc(t('select_customer'))+'</div>';
      document.getElementById('content').innerHTML=pageHead('quota_management','quota_desc','')+'<section class="panel"><div class="filterbar">'+customerPicker(customerId)+'</div>'+form+'</section><section class="panel"><div class="panel-head"><h2>'+esc(t('recent_activity'))+'</h2></div><div class="table-shell"><table><thead><tr><th>'+esc(t('event_id'))+'</th><th>'+esc(t('operation'))+'</th><th>'+esc(t('delta'))+'</th><th>'+esc(t('ledger_status'))+'</th><th>'+esc(t('created_at'))+'</th></tr></thead><tbody>'+recent+'</tbody></table></div></section>';
      bindCustomerPicker(renderQuota);
      if(customerId&&state.canWrite){var formEl=document.getElementById('quotaForm'),update=function(){quotaPreview(current,bps);};document.getElementById('quotaMode').onchange=update;document.getElementById('quotaUnit').onchange=update;document.getElementById('quotaAmount').oninput=update;update();formEl.onsubmit=function(e){e.preventDefault();submitQuota(customerId,current,bps,formEl);};}
    }
    function quotaDeltaFromForm(form,bps) {
      var data=Object.fromEntries(new FormData(form).entries()),raw=number(data.amount,0),delta;
      if(data.input_unit==='quota')delta=Math.trunc(raw);else delta=Math.trunc(raw/conversionRate()/(number(bps,10000)/10000)*quotaPerUnit());
      return {data:data,quota:Math.max(0,delta),signed:data.mode==='subtract'?-Math.max(0,delta):Math.max(0,delta)};
    }
    function quotaPreview(current,bps) {
      var form=document.getElementById('quotaForm'),v=quotaDeltaFromForm(form,bps),after=current+v.signed;
      document.getElementById('quotaPreview').innerHTML='<div class="detail-list"><dt>'+esc(t('current_balance'))+'</dt><dd>'+moneyQuota(current,bps)+'</dd><dt>'+esc(t('change_amount'))+'</dt><dd>'+moneyQuota(v.signed,bps)+'</dd><dt>'+esc(t('after_balance'))+'</dt><dd>'+moneyQuota(after,bps)+'</dd><dt>'+esc(t('conversion_snapshot'))+'</dt><dd>'+esc(pick(state.conversion,['display_type','currency','display_currency'],'USD'))+' · '+esc(t('quota_per_unit'))+' '+formatNumber(quotaPerUnit())+' · '+esc(t('exchange_rate'))+' '+conversionRate()+' · '+esc(t('discount'))+' '+discountText(bps)+'</dd></div>'+(v.data.mode==='subtract'?'<div class="notice danger" style="margin-top:12px">'+esc(t('subtract_guard'))+'</div>':'');
    }
    function submitQuota(customerId,current,bps,form) {
      var v=quotaDeltaFromForm(form,bps);if(!v.quota){toast(t('required'),'error');return;}var after=current+v.signed;
      var body='<div class="notice '+(v.data.mode==='subtract'?'danger':'info')+'">'+esc(v.data.mode==='subtract'?t('confirm_subtract'):t('idempotency'))+'</div><div class="detail-list"><dt>'+esc(t('select_customer'))+'</dt><dd>'+esc(pick(selectedCustomer(),['display_name','name'],'#'+customerId))+'</dd><dt>'+esc(t('current_balance'))+'</dt><dd>'+moneyQuota(current,bps)+'</dd><dt>'+esc(t('operation'))+'</dt><dd>'+esc(t(v.data.mode))+'</dd><dt>'+esc(t('change_amount'))+'</dt><dd>'+moneyQuota(v.signed,bps)+'</dd><dt>'+esc(t('after_balance'))+'</dt><dd>'+moneyQuota(after,bps)+'</dd><dt>'+esc(t('standard_amount'))+'</dt><dd>'+esc(money(standardMoney(v.quota)))+'</dd><dt>'+esc(t('discounted_amount'))+'</dt><dd>'+esc(money(customerMoney(v.quota,bps)))+'</dd><dt>'+esc(t('reason'))+'</dt><dd>'+esc(v.data.reason)+'</dd><dt>'+esc(t('conversion_snapshot'))+'</dt><dd>'+esc(pick(state.conversion,['display_type','currency','display_currency'],'USD'))+' · '+formatNumber(quotaPerUnit())+' quota · '+discountText(bps)+'</dd></div><div class="notice" style="margin-top:12px">'+esc(t('in_flight_warning'))+'</div>';
      openConfirm(v.data.mode==='subtract'?t('confirm_danger'):t('quota_management'),body,async function(btn){var eventId=uuidv7(),payload={mode:v.data.mode,input_unit:v.data.input_unit,amount:String(v.data.amount),reason:v.data.reason,idempotency_key:eventId};await execute(btn,function(){return api(API+'/customers/'+customerId+'/quota-adjustments',{method:'POST',body:payload,eventId:eventId});});closeConfirm();toast(t('operation_success'),'success');renderQuota();},t(v.data.mode));
    }

    async function renderLedger() {
      await prepareCustomerPage();var customerId=state.selectedCustomerId,items=[];
      if(customerId){var data=await api(API+'/customers/'+customerId+'/quota-ledger'+query({page:1,page_size:100}));items=listOf(data,['items','ledger','records']);}
      var bps=pick(selectedCustomer(),['effective_discount_bps','discount_bps'],10000);
      var rows=items.map(function(e){var delta=number(pick(e,['quota_delta','signed_quota_delta','delta_quota'],0)),status=String(pick(e,['status'],'-'));return '<tr><td class="mono wrap">'+esc(pick(e,['event_id'],'-'))+'</td><td>'+statusBadge(pick(e,['operation','mode'],'-'))+'</td><td>'+moneyQuota(pick(e,['quota_before'],0),bps)+'</td><td>'+moneyQuota(delta,bps)+'</td><td>'+moneyQuota(pick(e,['quota_after'],0),bps)+'</td><td>'+statusBadge(status)+'</td><td>'+esc(pick(e,['actor_name','actor_user_id'],'-'))+'</td><td class="wrap">'+esc(pick(e,['reason'],'-'))+'</td><td>'+esc(pick(e,['currency_type_snapshot','currency_snapshot','currency'],'-'))+'<span class="subvalue">'+discountText(pick(e,['discount_bps_snapshot'],bps))+'</span></td><td>'+formatDate(pick(e,['created_at'],''))+'</td><td>'+(state.canWrite&&status==='applied'?'<button class="button danger compact" data-reverse-event="'+attr(pick(e,['event_id'],''))+'" data-reverse-delta="'+attr(delta)+'">'+esc(t('reverse'))+'</button>':'')+'</td></tr>';}).join('')||emptyRow(11);
      document.getElementById('content').innerHTML=pageHead('quota_ledger','ledger_desc','')+'<section class="panel"><div class="filterbar">'+customerPicker(customerId)+'</div><div class="table-shell"><table style="min-width:1380px"><thead><tr><th>'+esc(t('event_id'))+'</th><th>'+esc(t('operation'))+'</th><th>'+esc(t('before'))+'</th><th>'+esc(t('delta'))+'</th><th>'+esc(t('after'))+'</th><th>'+esc(t('ledger_status'))+'</th><th>'+esc(t('actor'))+'</th><th>'+esc(t('reason'))+'</th><th>'+esc(t('conversion_snapshot'))+'</th><th>'+esc(t('created_at'))+'</th><th>'+esc(t('actions'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      bindCustomerPicker(renderLedger);
      document.querySelectorAll('[data-reverse-event]').forEach(function(b){b.onclick=function(){var delta=number(b.dataset.reverseDelta),mode=delta>0?'subtract':'add',eventId=b.dataset.reverseEvent;openConfirm(t('confirm_danger'),'<div class="notice danger">'+esc(t('confirm_reverse'))+'</div>',async function(btn){var requestId=uuidv7();await execute(btn,function(){return api(API+'/customers/'+customerId+'/quota-adjustments',{method:'POST',body:{mode:mode,input_unit:'quota',amount:String(Math.abs(delta)),reason:'reverse '+eventId,idempotency_key:requestId,reverses_event_id:eventId},eventId:requestId});});closeConfirm();toast(t('operation_success'),'success');renderLedger();},t('reverse'));};});
    }

    async function renderUsage() {
      await prepareCustomerPage();var f=window.__usageFilters||{},customerId=state.selectedCustomerId,items=[];
      if(customerId){var data=await api(API+'/customers/'+customerId+'/usage'+query({start_time:f.start_time,end_time:f.end_time,model:f.model,token_id:f.token_id,page:1,page_size:100}));items=listOf(data,['items','usage','records','logs']);}
      var bps=pick(selectedCustomer(),['effective_discount_bps','discount_bps'],10000);
      var rows=items.map(function(u){var q=pick(u,['quota','standard_quota'],0);return '<tr><td>'+formatDate(pick(u,['created_at','time'],''))+'</td><td class="mono">'+esc(pick(u,['log_id','id'],'-'))+'</td><td>'+esc(pick(u,['model_name','model'],'-'))+'</td><td class="mono">'+esc(pick(u,['token_id'],'-'))+'</td><td>'+formatNumber(pick(u,['prompt_tokens'],0))+'</td><td>'+formatNumber(pick(u,['completion_tokens'],0))+'</td><td>'+moneyQuota(q,bps)+'</td><td>'+esc(money(pick(u,['discounted_amount'],customerMoney(q,bps))))+'</td><td>'+esc(pick(u,['channel_id'],'-'))+'</td></tr>';}).join('')||emptyRow(9);
      document.getElementById('content').innerHTML=pageHead('usage_title','usage_desc','')+'<section class="panel"><div class="filterbar">'+customerPicker(customerId)+'<div class="field"><label>'+esc(t('start_time'))+'</label><input id="usageStart" type="datetime-local" value="'+attr(f.start_time||'')+'"></div><div class="field"><label>'+esc(t('end_time'))+'</label><input id="usageEnd" type="datetime-local" value="'+attr(f.end_time||'')+'"></div><div class="field"><label>'+esc(t('model'))+'</label><input id="usageModel" value="'+attr(f.model||'')+'"></div><div class="field"><label>'+esc(t('token_id'))+'</label><input id="usageToken" type="number" min="1" value="'+attr(f.token_id||'')+'"></div><button class="button" id="applyUsage">'+esc(t('apply'))+'</button></div><div class="table-shell"><table style="min-width:1150px"><thead><tr><th>'+esc(t('created_at'))+'</th><th>'+esc(t('log_id'))+'</th><th>'+esc(t('model'))+'</th><th>'+esc(t('token_id'))+'</th><th>'+esc(t('prompt_tokens'))+'</th><th>'+esc(t('completion_tokens'))+'</th><th>'+esc(t('standard_usage'))+'</th><th>'+esc(t('discounted_amount'))+'</th><th>'+esc(t('channel_id'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      bindCustomerPicker(renderUsage);document.getElementById('applyUsage').onclick=function(){window.__usageFilters={start_time:document.getElementById('usageStart').value,end_time:document.getElementById('usageEnd').value,model:document.getElementById('usageModel').value,token_id:document.getElementById('usageToken').value};renderUsage();};
    }

    async function renderAudit() {
      var data=await api(API+'/audit-logs'+query({page:1,page_size:100,reseller_id:state.isRoot?state.selectedResellerId:''}));var items=listOf(data,['items','audit_logs','records']);if(state.isRoot)await loadResellers();
      var rows=items.map(function(a){return '<tr><td>'+formatDate(pick(a,['created_at'],''))+'</td><td>'+esc(pick(a,['actor_name','actor_user_id'],'-'))+'</td><td>'+esc(pick(a,['action'],'-'))+'</td><td>'+esc(pick(a,['object_type','target_type'],'-'))+'<span class="subvalue mono">'+esc(pick(a,['object_id','target_id'],'-'))+'</span></td><td class="wrap">'+esc(pick(a,['detail_json','detail','summary','reason'],'-'))+'</td><td class="mono">'+esc(pick(a,['request_id'],'-'))+'</td><td>'+esc(pick(a,['ip_address','ip'],'-'))+'</td></tr>';}).join('')||emptyRow(7);
      var rootFilter=state.isRoot?'<div class="filterbar"><div class="field"><label>'+esc(t('select_reseller'))+'</label><select id="auditReseller">'+resellerOptions(state.selectedResellerId)+'</select></div></div>':'';
      document.getElementById('content').innerHTML=pageHead('audit_title','audit_desc','')+'<section class="panel">'+rootFilter+'<div class="table-shell"><table style="min-width:1200px"><thead><tr><th>'+esc(t('created_at'))+'</th><th>'+esc(t('actor'))+'</th><th>'+esc(t('action'))+'</th><th>'+esc(t('target'))+'</th><th>'+esc(t('detail'))+'</th><th>'+esc(t('request_id'))+'</th><th>'+esc(t('ip'))+'</th></tr></thead><tbody>'+rows+'</tbody></table></div></section>';
      if(state.isRoot)document.getElementById('auditReseller').onchange=function(e){state.selectedResellerId=e.target.value;renderAudit();};
    }

    document.getElementById('navigation').addEventListener('click',function(e){var b=e.target.closest('[data-page]');if(b)navigate(b.dataset.page);});
    document.getElementById('menuButton').onclick=function(){document.body.classList.toggle('menu-open');};
    document.getElementById('overlay').onclick=function(){document.body.classList.remove('menu-open');};
    document.getElementById('refreshButton').onclick=async function(){try{state.reference=Object.assign({},state.reference,await api(API+'/reference'));state.conversion=Object.assign({},state.conversion,await api(API+'/quota-conversion-config'));}catch(err){toast(err.message,'error');}renderPage();};
    document.querySelectorAll('[data-language]').forEach(function(b){b.onclick=function(){state.lang=b.dataset.language;localStorage.setItem('reseller_hub_language',state.lang);renderChrome();renderPage();};});
    document.querySelectorAll('[data-close-modal]').forEach(function(b){b.onclick=closeForm;});
    document.querySelectorAll('[data-close-confirm]').forEach(function(b){b.onclick=closeConfirm;});
    document.getElementById('modalForm').onsubmit=async function(e){e.preventDefault();if(!modalSubmit)return;var button=document.getElementById('submitButton');try{await modalSubmit(e.currentTarget,button);}catch(_){}};
    document.getElementById('confirmButton').onclick=async function(){if(!confirmSubmit)return;try{await confirmSubmit(this);}catch(_){}};
    document.getElementById('copySecret').onclick=async function(){var input=document.getElementById('secretValue');try{await navigator.clipboard.writeText(input.value);}catch(_){input.select();document.execCommand('copy');}this.textContent=t('copied');};
    document.getElementById('closeSecret').onclick=closeSecret;
    document.addEventListener('keydown',function(e){if(e.key==='Escape'){closeForm();closeConfirm();}});

    async function boot() {
      try {
        var me=await api(API+'/me');state.me=me||{};state.csrf=pick(me,['csrf_token','csrf'],'');
        var role=String(pick(me,['hub_role','reseller_role','role_name'],'')).toLowerCase(),numericRole=number(pick(me,['new_api_role','role'],0),0);
        state.isRoot=role==='hub_super_admin'||role==='root'||numericRole>=100;
        state.canWrite=state.isRoot||role==='reseller_admin';
        try { state.conversion=Object.assign(state.conversion,await api(API+'/quota-conversion-config')); } catch(err) { toast(err.message,'error'); }
        try { state.reference=Object.assign(state.reference,await api(API+'/reference')); } catch(err) { toast(err.message,'error'); }
        var hash=location.hash.replace('#','');state.page=(state.isRoot?rootNavigation:resellerNavigation).indexOf(hash)>=0?hash:'dashboard';
        renderChrome();await renderPage();
      } catch(err) { document.getElementById('content').innerHTML='<div class="notice danger">'+esc(err.message||t('request_failed'))+'</div>'; }
    }
    boot();
  })();
  </script>
</body>
</html>`
