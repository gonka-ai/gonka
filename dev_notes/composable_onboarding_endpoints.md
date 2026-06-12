# Composable Onboarding:fact endpoints 的来龙去脉

记录 PR #1296(Onboarding Clarity)收到 reviewer 可组合性建议后,新增三个只读 fact
endpoints 的完整决策过程:评论原文与译文、我们的应对方案、新增端点清单、测试结果、
拟定的回复;附录为逐字节的 curl 测试 transcript。

---

## 1. Reviewer 评论原文

@patimen(John Long,#866 的 reviewer 之一)于 2026-06-12 在 PR #1296 评论:

> @bonujel If I may offer an idea:
> What if we leaned a bit more toward composability here?
>
> I wonder if, instead of putting more onboarding-specific state/behavior into
> the runtime, we could expose a few small factual queries/actions and let the
> onboarding flow be composed on top of them, either by a human operator or by
> an AI `skill.md`
>
> A small example from this PR would be the difference between:
> - teaching the system a richer onboarding state machine like TESTING /
>   TEST_FAILED / auto-retry / "safe to be offline"
> vs.
> - exposing simpler facts like "what nodes are configured?", "what does the
>   broker currently think about them?", "how long until PoC?", or "what
>   launch plan would this node use right now?"
>
> Then the higher-level onboarding guidance becomes something layered on top
> of those primitives, instead of baked into the server itself.
> My instinct is that this would give us something more flexible and reusable,
> without committing the runtime to as much onboarding-specific logic.

## 2. 评论译文

> 容我提个想法:我们在这里是不是可以更偏向「可组合性」一点?
>
> 我在想,与其往 runtime 里塞进更多 onboarding 专用的状态和行为,我们能不能只暴露
> 几个小而事实性的查询/动作,让 onboarding 流程在它们之上「组合」出来——既可以由
> 人工运营者组合,也可以由一个 AI 的 `skill.md` 组合。
>
> 拿这个 PR 举个小例子,区别在于:
> - 教会系统一套更丰富的 onboarding 状态机,比如 `TESTING` / `TEST_FAILED` /
>   自动重试 / "safe to be offline";
> - 还是只暴露更简单的事实:"配置了哪些节点?"、"broker 现在怎么看它们?"、
>   "距离 PoC 还有多久?"、"这个节点现在会用什么 launch plan?"。
>
> 这样,高层的 onboarding 指引就成了叠加在这些原语之上的一层,而不是焊死在
> server 内部。我的直觉是,这会给我们更灵活、更可复用的东西,也不必让 runtime
> 背上那么多 onboarding 专用逻辑。

要点:这是方向性建议(措辞是 "If I may / I wonder / my instinct"),不是 merge
blocker——同期他还在为该 PR 触发集成测试、两次把 base 合入分支。其本质是 #866
评审哲学("别把 UX 焊进核心")的延伸:server 只报事实,判断和话术放到上层。

## 3. 我们的方案:先补齐原语,不做大改,组合层留给后续 PR

### 3.1 为什么不现在大改

1. **大改会让 Issue #326 无法解决。** #326 的验收点本身就是 runtime 行为:
   "最显眼的日志应是 waiting for PoC(周期性打印)"、"新 MLnode 注册后**自动**
   测试"。周期日志只能由进程自己打,自动测试需要进程内触发器(我们挂在区块分发
   器的 synced epoch 事件上)。外部的 skill.md 无法常驻在节点进程里完成这两件事
   ——把派生层/自动测试从 server 里删掉,等于重新定义 Issue 而不是解决它。
2. **返工成本与风险不成比例。** 全面转向需删改约 480 行生产代码、重写约 650 行
   测试,并重置一个 +3972 行、已评审两周的 PR 的 review 进度。
3. **本 PR 已经做对了一半。** `GET /nodes` 的 `onboarding` 块是**纯只读派生**
   (由 broker 事实 + 链相位 + 缓存活跃度 + 最近测试结果计算而来),0 状态写回
   broker/runtime。它本质上就是"叠加在原语之上的便利层",将来要摘除**不需要任何
   迁移**——与 patimen 设想的分层并不冲突,只是我们把便利层一并附送了。

### 3.2 实际做法:把"事实原语"显式暴露出来

盘点 patimen 点名的原语,大部分本已存在:"配置了哪些节点 / broker 怎么看它们"
(`GET /nodes` 原始视图)、环境就绪清单(`GET /setup/report`)。缺口集中在三处,
于是新增三个**只读** fact endpoints(见 §4)补齐——其中 launch-plan 正是他亲口
举的例子。

关键性质:

- **全部只读**,不写任何 broker/runtime 状态;
- **全部组合自本 PR 自己引入的构件**(`broker.BuildModelLaunchPlan` /
  `MLNodeTester.LastResult` / `ComputeTiming`),对历史代码零修改,`server.go`
  仅追加 3 行路由——不扩大 runtime 的改动面;
- launch-plan 与实际测试/broker **共用同一条模型筛选管线**(PoC-params 过滤 →
  治理过滤 → 治理+本地参数合并),"预览"与"实际启动"不会漂移。

### 3.3 后续路线

这些端点使 onboarding 流程从此**可以**被组合:后续 PR 可提供一份 `skill.md`
(或运营 runbook),由人工或 AI 依次查询这些事实原语,自行推导"该等待/该修复/
可关机"的指引。届时若团队倾向更薄的 server,派生的状态名与话术层可以直接摘除
(无迁移成本),onboarding 指引整体迁移到组合层——本次新增的端点就是那条路的
地基。换言之:**现在解决 Issue,同时为 composability 铺路,两者不冲突。**

## 4. 新增 endpoints

| Endpoint | 返回的事实 | 复用的构件 |
|---|---|---|
| `GET /admin/v1/nodes/:id/launch-plan` | 该节点**现在**会启动哪些模型及最终合并参数(`plans[].args`);不会启动的模型及原因:`skipped_models`(不在治理)、`unsupported_models`(不被当前 PoC params 支持) | `broker.BuildModelLaunchPlan`(与 broker/测试同管线) |
| `GET /admin/v1/nodes/:id/test` | 最近一次 MLnode 测试的**原始** `TestResult`(`POST` 负责"做",`GET` 负责"查"):状态、各模型加载耗时、失败模型、错误、是否可重试 | `MLNodeTester.LastResult` / `IsRunning` |
| `GET /admin/v1/poc/timing` | 链相位计时事实:当前相位、距下次 PoC 的块数/秒数、是否应在线;未同步时返回 `available:false` 而非误导性的 0 | `ComputeTiming` |

状态码约定:`200` 事实返回(含 `last:null` / `available:false`——"暂无数据"
本身也是事实);`404` 节点不在配置中;`502` 仅 launch-plan:治理模型链上查询失败
(上游依赖故障)。

改动量:+312 行(实现 155 行 + 测试 157 行),修改 4 个文件,全部在分支
`onboarding_clarity_1261-add_endpoints`(commit `76e050ff`)。

## 5. 测试与结果

两层验证,全部通过:

**单元/回归(本地 arm64 + x86 native amd64):**

- 新增 3 个测试函数、8 个 subtest 全 PASS(合并参数、skipped 上报、404、
  无结果→null、原始结果回读、synced/未 synced 两态);
- 全量回归 `admin` / `selfcheck` / `broker` / `event_listener` / `participant`
  全绿;`go build` / `go vet` / `gofmt` 干净。

**真实 HTTP 冒烟(x86 native amd64,真 TCP + 系统 curl):**

harness 将真实 admin echo server 绑定 `127.0.0.1:19123`,fixture 为一个节点配
三个模型(分别命中 plan / skipped / unsupported 三分支),链 mock 且 synced、
下次 PoC 在 9999 块外,MLnode mock。8 个场景返回逐字采集(完整 transcript 见
文末附录),结果 `--- PASS: TestFactEndpointsCurlSmoke`,exit 0;本地 arm64
复现一致:

| 场景 | HTTP | 实际返回 |
|---|---|---|
| A. timing(已同步) | 200 | `{"available":true,"timing":{"current_phase":"Inference","blocks_until_next_poc":9999,"seconds_until_next_poc":59994,"should_be_online":false}}` |
| B. launch-plan(三分支) | 200 | `{"node_id":"mlnode-1","plans":[{"model_id":"test-model","args":["--max-model-len","8192"]}],"skipped_models":["legacy-model"],"unsupported_models":["old-model"]}` |
| C. 测试结果(未测过) | 200 | `{"last":null,"node_id":"mlnode-1","running":false}` |
| D. POST 跑一次测试 | 200 | `{"node_id":"mlnode-1","status":"SUCCESS","load_ms":{"test-model":0},"started_at":"2026-06-12T15:41:10+08:00","duration_ms":0}` |
| E. 测试结果(回读) | 200 | `{"last":{…同 D 的原始结果…},"node_id":"mlnode-1","running":false}` |
| F. 未知节点 launch-plan | 404 | `{"error":"node not configured","node_id":"no-such-node"}` |
| G. 未知节点 test | 404 | `{"error":"node not configured","node_id":"no-such-node"}` |
| H. timing(未同步) | 200 | `{"available":false}` |

复跑方式:`go test -tags factsmoke -run TestFactEndpointsCurlSmoke -v
./internal/server/admin/`(harness 由 `factsmoke` build tag 守卫,常规测试套件
不会执行)。

## 6. 拟定给 patimen 的回复(贴 PR 用,英文)

要点:认同方向 → 派生层本就是只读可摘的便利层 → 用一张映射表把**完整原语集**
对应到他点名的问题(标注本 PR 新增的三个)→ 说明为什么本 PR 保留现有派生层
(#326 的必要性:周期日志与自动测试须在进程内、开箱即用的清晰状态正是 issue 所
要求的)→ skill.md 组合层作为 follow-up,验证后派生层可无迁移摘除。

> @patimen Thanks — I like this direction, and the PR is closer to it than it
> may look: the `onboarding` block is a read-only derivation (nothing is
> written back to the broker or stored as runtime state), so the state names
> and wording are just a convenience layer over the facts, removable later
> without migration.
>
> To make the primitives explicit, I've added three read-only fact endpoints
> to this PR (built from pieces it already introduces, no new runtime state).
> With those, the full primitive set maps onto your questions like this:
>
> | Primitive (fact / action) | Endpoint |
> |---|---|
> | What nodes are configured / what does the broker currently think about them? | `GET /nodes` (raw broker view; the `onboarding` block is derived on top of it) |
> | How long until PoC? | `GET /poc/timing` **(new)** — phase, blocks/seconds until next PoC; `available:false` until synced |
> | What launch plan would this node use right now? | `GET /nodes/:id/launch-plan` **(new)** — final merged args per model, plus configured models that would *not* launch and why; same selection pipeline as the broker, so the preview cannot drift |
> | Run a readiness test / read the raw result back | `POST /nodes/:id/test` / `GET /nodes/:id/test` **(GET new)** — raw `TestResult` with per-model load times, failing model, retryability |
> | Is the environment set up correctly? | `GET /setup/report` |
>
> On keeping the derived layer in this PR: it is what actually delivers #326.
> The issue asks the node itself to make "waiting for PoC" the most visible
> log, to test new MLnodes automatically, and to explain benign states — the
> periodic status line and the auto-test trigger can only run in-process, and
> the out-of-the-box wording is what gives operators the clarity #326 asks
> for without requiring any extra tooling. Dropping it now would leave the
> issue unsolved for anyone who just reads logs and calls the API.
>
> As the follow-up, I'd like to build the composition layer you describe: a
> `skill.md` that derives the onboarding guidance from these primitives. If
> that proves out, the derived wording layer here can be thinned or removed
> without migration, since nothing behind it is stored.

## 附录:8 个场景的 verbatim curl transcript

采集于 2026-06-12,x86 native linux/amd64,分支
`onboarding_clarity_1261-add_endpoints`(commit `76e050ff`);响应为真实 TCP
线上输出,逐字未改(仅省略 `Date`/`Content-Length` 头)。

### A. PoC timing — chain synced

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/poc/timing
HTTP/1.1 200 OK
Content-Type: application/json

{"available":true,"timing":{"current_phase":"Inference","blocks_until_next_poc":9999,"seconds_until_next_poc":59994,"should_be_online":false}}
```

### B. Launch plan — plan + skipped + unsupported 三分支同响应

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/nodes/mlnode-1/launch-plan
HTTP/1.1 200 OK
Content-Type: application/json

{"node_id":"mlnode-1","plans":[{"model_id":"test-model","args":["--max-model-len","8192"]}],"skipped_models":["legacy-model"],"unsupported_models":["old-model"]}
```

`plans[].args` 是最终合并命令(治理参数 + 节点本地参数)——与
`InferenceUpNodeCommand` 发给 MLnode 的完全一致。

### C. Last test result — 尚未测试过

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/nodes/mlnode-1/test
HTTP/1.1 200 OK
Content-Type: application/json

{"last":null,"node_id":"mlnode-1","running":false}
```

### D. POST 跑一次测试(load → health → inference probe)

```
$ curl -s -i -X POST http://127.0.0.1:19123/admin/v1/nodes/mlnode-1/test
HTTP/1.1 200 OK
Content-Type: application/json

{"node_id":"mlnode-1","status":"SUCCESS","load_ms":{"test-model":0},"started_at":"2026-06-12T15:41:10.037900007+08:00","duration_ms":0}
```

### E. Last test result — 原始结果回读

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/nodes/mlnode-1/test
HTTP/1.1 200 OK
Content-Type: application/json

{"last":{"node_id":"mlnode-1","status":"SUCCESS","load_ms":{"test-model":0},"started_at":"2026-06-12T15:41:10.037900007+08:00","duration_ms":0},"node_id":"mlnode-1","running":false}
```

FAILED 时 `last` 额外携带 `failing_model`、`error`、`retryable`(瞬时 vs 确定性
失败),字段定义见 `TestResult`。

### F/G. 未知节点 — 两个 GET 端点均 404

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/nodes/no-such-node/launch-plan
HTTP/1.1 404 Not Found
Content-Type: application/json

{"error":"node not configured","node_id":"no-such-node"}
```

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/nodes/no-such-node/test
HTTP/1.1 404 Not Found
Content-Type: application/json

{"error":"node not configured","node_id":"no-such-node"}
```

### H. PoC timing — chain NOT synced

```
$ curl -s -i -X GET http://127.0.0.1:19123/admin/v1/poc/timing
HTTP/1.1 200 OK
Content-Type: application/json

{"available":false}
```

返回 `available:false` 而非清零的数字,消费方不会把"时间表未知"误读成
"PoC 已迫近"。
