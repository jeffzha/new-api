import type { Locale } from "../../lib/types";

const rows = [
  ["Live health checks for services, queues and data", "实时检查代理商服务、队列和数据结构", "即時檢查代理商服務、佇列與資料結構", "Contrôles en direct des services, files et données", "サービス、キュー、データの稼働状況を確認", "Проверка сервисов, очередей и данных в реальном времени", "Kiểm tra trực tiếp dịch vụ, hàng đợi và dữ liệu"],
  ["All systems operational", "服务运行正常", "服務運行正常", "Tous les systèmes fonctionnent", "すべてのシステムは正常です", "Все системы работают нормально", "Tất cả hệ thống đang hoạt động"],
  ["Schema needs attention", "数据结构需要处理", "資料結構需要處理", "Le schéma nécessite une intervention", "スキーマの確認が必要です", "Требуется проверить схему", "Cần xử lý cấu trúc dữ liệu"],
  ["Enabled capabilities", "已启用能力", "已啟用能力", "Fonctionnalités activées", "有効な機能", "Включённые возможности", "Tính năng đã bật"],
  ["Open issues", "待处理对账", "待處理對帳", "Incidents ouverts", "未処理の照合", "Открытые проблемы", "Vấn đề đang chờ xử lý"],
  ["Live availability of agency services", "代理商中心各项服务的实时可用状态", "代理商中心各項服務的即時可用狀態", "Disponibilité en direct des services agence", "代理店サービスの稼働状況", "Текущая доступность сервисов агентства", "Trạng thái khả dụng của dịch vụ đại lý"],
  ["Background jobs and synchronization queues", "后台任务和数据同步队列", "後台任務與資料同步佇列", "Tâches en arrière-plan et files de synchronisation", "バックグラウンド処理と同期キュー", "Фоновые задачи и очереди синхронизации", "Tác vụ nền và hàng đợi đồng bộ"],
  ["Needs attention", "需要关注", "需要關注", "À surveiller", "要確認", "Требует внимания", "Cần chú ý"],
  ["Clear", "运行正常", "運行正常", "Aucun élément en attente", "正常", "Всё в порядке", "Bình thường"],
  ["All queues are clear", "所有队列均正常", "所有佇列均正常", "Toutes les files sont normales", "すべてのキューは正常です", "Все очереди в норме", "Tất cả hàng đợi đều bình thường"],
  ["Agency data service", "代理商数据服务", "代理商資料服務", "Service de données agence", "代理店データサービス", "Сервис данных агентства", "Dịch vụ dữ liệu đại lý"],
  ["Billing component", "计费组件", "計費元件", "Composant de facturation", "課金コンポーネント", "Компонент биллинга", "Thành phần tính phí"],
  ["Billing data schema", "计费数据结构", "計費資料結構", "Schéma de facturation", "課金データ構造", "Схема данных биллинга", "Cấu trúc dữ liệu tính phí"],
  ["Commission processing", "佣金处理", "佣金處理", "Traitement des commissions", "コミッション処理", "Обработка комиссий", "Xử lý hoa hồng"],
  ["Data exports", "数据导出", "資料匯出", "Exports de données", "データエクスポート", "Экспорт данных", "Xuất dữ liệu"],
  ["Usage projection", "用量统计", "用量統計", "Projection de consommation", "利用量集計", "Расчёт использования", "Thống kê mức sử dụng"],
  ["Agency onboarding", "代理商注册", "代理商註冊", "Inscription agence", "代理店登録", "Подключение агентства", "Đăng ký đại lý"],
  ["Message queue", "消息队列", "訊息佇列", "File de messages", "メッセージキュー", "Очередь сообщений", "Hàng đợi tin nhắn"],
  ["Pricing snapshot", "价格快照", "價格快照", "Instantané des prix", "価格スナップショット", "Снимок цен", "Ảnh chụp giá"],
  ["Commission withdrawals", "佣金提现", "佣金提現", "Retraits de commissions", "コミッション出金", "Вывод комиссий", "Rút hoa hồng"],
  ["Processing", "处理中", "處理中", "En traitement", "処理中", "Обрабатывается", "Đang xử lý"],
  ["Pending", "待处理", "待處理", "En attente", "保留中", "Ожидает", "Đang chờ"],
  ["Failed jobs", "异常任务", "異常任務", "Tâches en échec", "異常タスク", "Ошибочные задачи", "Tác vụ lỗi"],
  ["Retrying", "等待重试", "等待重試", "Nouvelle tentative", "再試行待ち", "Ожидает повтора", "Chờ thử lại"],
  ["Open reconciliation", "待处理对账", "待處理對帳", "Rapprochements ouverts", "未処理の照合", "Открытые сверки", "Đối soát đang chờ"],
  ["Exports in progress", "导出处理中", "匯出處理中", "Exports en cours", "エクスポート中", "Экспорт выполняется", "Đang xuất dữ liệu"],
] as const;

const locales: Locale[] = ["en", "zh", "zh-TW", "fr", "ja", "ru", "vi"];
export const statusMessages = Object.fromEntries(
  locales.map((locale, index) => [locale, Object.fromEntries(rows.map((row) => [row[0], row[index]]))]),
) as Record<Locale, Record<string, string>>;
