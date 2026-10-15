# Forward Direct Wallet Receipts

This opt-in slice retains the existing quota calculators. It changes financial
persistence and rejects paths without an accepted forward identity. Flags:
`OPS_CN_FINANCE_V2_ENABLED=true`, `OPS_CN_DURABLE_WALLET_ENABLED=true`,
`OPS_CN_DURABLE_DIRECT_WALLET_ENABLED=true`. Redis and batch must be disabled.
An enabled direct flag never authorizes a fallback to buffered wallet writes.

## Exact Entry Scope

- `controller/relay.go` and `relay/PrepareRequestBilling`: OpenAI text/audio
  forward free-model quotes that skipped the normal session. Before upstream
  I/O, `BeginCNDirectRelay` binds request/user/token/channel and freezes the
  evidenced currency conversion. `PostTextConsumeQuota` / `PostAudioConsumeQuota`
  bind the actual usage to the dedicated settler; `PostConsumeQuota` accepts only
  that bound action. Existing nonzero preconsumption without a receipt is rejected.
- OpenAI realtime with `wallet_only`, ordinary account, real token/channel,
  ratio pricing, no tiered expression or playground. `PreConsumeBilling` installs
  a dedicated settler; `WssHelper` validates it before upstream I/O, including the
  zero-preconsume path. Initial wallet and token reserve commit together.
- The actual `OpenaiRealtimeHandler` forwards cumulative upstream usage through
  `preConsumeUsageWithCumulative` to the durable admission API. This preserves the
  existing handler's cumulative/monotonic usage policy. It does not certify
  providers that use per-response counters or reset counters during a session.
  Delta-only `PreWssConsumeQuotaWithResult` and locally estimated trailing usage
  cannot invent a segment identity in this mode.

The permanent segment key hashes request plus canonical cumulative usage; the
receipt stores the exact live quota and resulting absolute reserved target.
Replays return the original receipt without consulting current model prices.
A reconstructed process loads the latest accepted cumulative usage from the DB.
Fresh segments use the existing live calculator, not a new pricing policy.

Each segment atomically changes wallet/token and records its receipt. Those
segments are reservations pending a terminal request decision, not additional
cash/revenue posting authorities. Terminal wallet/token reconciliation, real
user/channel usage counters, request count, terminal receipt and charge outbox
commit together. Only the published canonical charge is accounting authority.

Cancellation decisions are durable and recovered by the existing wallet recovery
loop. They release unused initial reservation while retaining successful segment
charges. Active requests never expire automatically. An uncommitted final target
requires replay or an explicit cancellation decision; recovery never prices old
usage from today's settings. Request and segment receipts have no cleanup path.

## Evidence And Coverage

Wire evidence: `gateway-cn` / `cn_direct_wallet_final` / request ID, with immutable
final-decision hash. Kind `charge`, billing source `wallet`, request economic ID.
Conversion is frozen before I/O. Missing conversion stays null with unknown money,
even if valid currency settings return later. Nominal converted quota is not
recognized revenue, paid value or a reconstructed payment.

Source proof dependency `ordinary_wallet_direct_realtime` measures rows, segment
sums, terminal receipts, publication, pending cancellations, conversion unknowns
and usage gaps. Supported paths:
`ordinary-relay/forward-free-quote-final-v1` and
`ordinary-realtime/cumulative-absolute-target-v1`. S5 must register those paths
for `wallet` / `cn_direct_wallet_final`, only `charge`, namespace `gateway-cn`.
This implementation does not modify the S5 checkout or adopt an authority.

The broader legacy/direct/history, Redis/batch, missing receipt, violation fee,
subscription, playground, channel-switch retry, realtime local estimates,
fixed-price realtime and tiered realtime paths remain excluded. The global
historical/unsupported dependencies remain gaps. Normal paid BillingSession
wallet/token atomic finalization is a separate batch; this slice does not claim
it is fixed. Midjourney code and dedicated modules were not changed.

## Verification

Real engines: SQLite 3.50.4, MySQL 8.0.39, PostgreSQL 17.6. Isolated databases:
`s4r_direct_v1`, `s4r_direct_service_v1`, `s4r_direct_entry_v1`, local ports
53394/55494. All evidence is under the owning outer worktree `data/`.

- `go test ./model -run '^TestCNDirect' -v -count=1 -timeout=180s`:
  `s4r-direct-model-three-db.log`. Fresh migration and repeated upgrade from S4
  schema with retained live receipt; initial/segment/outbox failure rollback;
  duplicate and concurrent segment replay; reconstructed DB handles; conflicting
  final target; durable cancel recovery; receipt retention; frozen/unknown
  conversion; actual publication and cursor replay. Three engines pass.
- `go test ./service -run '^TestCNDirect' -v -count=1 -timeout=180s`:
  `s4r-direct-service-three-db.log`. Real PreConsumeBilling/PostConsumeQuota and
  text settle entry, token/usage legs, zero/free request count, identity rejection,
  model-price changes on replay, Redis/batch guards, default legacy behavior.
  Three engines pass.
- `go test ./relay/channel/openai -run '^TestCNDirect' -v -count=1 -timeout=180s`:
  `s4r-direct-entry-three-db.log`. Actual websocket handler with local upstream
  cumulative frames 20, duplicate 20, 25, normal close, real finalizer and replay:
  one final debit 25, one request count, two retained segment receipts, identical
  token/user/channel usage. Three engines pass.
- Actual published `s4r-http-artifacts/direct-wallet-canonical-{sqlite,mysql,
  postgres}.json` each passes S5 `validate_fact` (read-only import). See
  `s4r-direct-s5-wire-validation.log`. Authority adoption remains an integration gate.

S4 prerequisite `b15ffcc24c4267e52d383521b3e7d70414478e5c` is present as local
cherry-pick `7ccad9c`. There were no production actions, pushes or deployments.
