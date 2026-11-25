package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAppJSGroupFilterAndBatchScope 分组筛选与「组内批量」的作用域（issue #159）。
//
// 为什么值得一测：组内批量是写操作（悬停一下就能把一批号撤出流量池）。若筛选没
// 真正生效、或批量谓词放宽成「全表」，界面看起来「只动了这一组」，实际动了整池——
// Go 侧全绿，JS 语法检查也发现不了。这里数真实渲染出来的行，而不是断言源码里有
// 某个字符串。
func TestAppJSGroupFilterAndBatchScope(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; group filter test skipped")
	}
	script := `const fs = require('fs'), vm = require('vm'), assert = require('assert');
const src = fs.readFileSync(process.argv[2], 'utf8');

// ── 1) 分区和分组决定批量范围，状态只筛选表格 ──
const s = src.indexOf('function renderAccounts('), e = src.indexOf('// renderModelLocks');
assert.ok(s >= 0 && e > s, 'renderAccounts block not found');
const tb = { innerHTML: '' };
const statusFilter = { value: '', innerHTML: '' };
const groupScope = { hidden: true };
const groupActions = { addEventListener(_type, callback) { this.click = callback; } };
const requests = [];
const visibleUIDs = () => [...tb.innerHTML.matchAll(/<tr[^>]+title="uid: ([^"]+)"/g)].map(m => m[1]);
const statusOptions = () => Object.fromEntries(
  [...statusFilter.innerHTML.matchAll(/<option value="([^"]*)">([^<]+)<\/option>/g)].map(([, value, text]) => [value, text])
);
const ctx = {
  String, Number, Math, Date, Object, Array, Set, JSON, RegExp, console,
  document: { activeElement: null },
  $: id => ({ accBody: tb, accStatusFilter: statusFilter, accGroupActions: groupActions, accGroupScope: groupScope }[id] || null),
  confirm: () => true,
  api: async (path, { method }) => { requests.push([method, path]); },
  toast: () => {}, loadOverview: () => {},
  setHTML: (el, html) => { el.innerHTML = html; },
  esc: x => String(x == null ? '' : x),
  remarkName: (uid, html) => html, dur: () => '1m', ago: () => '刚刚',
  rateLimitRowsHtml: () => '', formatTokenCount: () => '0',
  formatLatency: () => '-', formatRate: () => '-', accountProxyHTML: () => '',
  syncGroupFilter: () => {},   // 下拉重建只碰 DOM，本测试关心筛选与批量作用域
  accRealmFilter: 'all', lastAccountsList: [],
  accGroupFilter: '', groupBatchAccounts: [],
};
vm.createContext(ctx);
const bindingStart = src.indexOf("if ($('accStatusFilter'))");
const bindingEnd = src.indexOf('/* 实测上限标注', bindingStart);
assert.ok(bindingStart >= 0 && bindingEnd > bindingStart, 'account filter bindings not found');
const bindings = src.slice(bindingStart, bindingEnd);
vm.runInContext([src.slice(s, e), bindings, 'this.render = renderAccounts; this.actions = GROUP_ACT;'].join('\n'), ctx);
function selectStatus(value) {
  statusFilter.value = value;
  statusFilter.onchange({ currentTarget: statusFilter });
}
const acct = (uid, group) => ({
  uid, group, realm: 'cn', credits: 1, nickname: uid,
  success_count: 1, err_total: 0, in_flight: 0, paused: false, disabled: false,
});
const rows = [
  acct('a1', 'A'),
  { ...acct('a2', 'A'), paused: true },
  { ...acct('b1', 'B'), reserve_blocked: true },
  acct('c1', ''),
];

ctx.render(rows);
assert.deepStrictEqual(statusOptions(), {
  '': '全部状态 (4)', '可用': '可用 (2)', '已暂停选号': '已暂停选号 (1)', '保留积分': '保留积分 (1)',
});

ctx.accGroupFilter = 'A';
ctx.render(rows);
assert.deepStrictEqual(visibleUIDs(), ['a1', 'a2'], '组 A 应只显示组内账号');
assert.deepStrictEqual(statusOptions(), {
  '': '全部状态 (2)', '可用': '可用 (1)', '已暂停选号': '已暂停选号 (1)', '保留积分': '保留积分 (0)',
});
selectStatus('已暂停选号');
const mixedRealms = [...rows, { ...acct('a3', 'A'), realm: 'global' }];
ctx.render(mixedRealms);
assert.strictEqual(statusOptions()['可用'], '可用 (2)');
ctx.accRealmFilter = 'cn';
ctx.render(mixedRealms);
assert.deepStrictEqual(visibleUIDs(), ['a2']);
assert.strictEqual(statusOptions()['可用'], '可用 (1)');
assert.strictEqual(groupScope.hidden, false, '说明应提示组内批量范围');
const batch = groupActions.click({ target: { closest: () => ({ dataset: { gact: 'pause' } }) } });

ctx.render(rows.map(s => ({ ...s, paused: false })));
assert.strictEqual(statusFilter.value, '已暂停选号', '刷新后保留已选筛选');
assert.strictEqual(statusOptions()['已暂停选号'], '已暂停选号 (0)');
assert.deepStrictEqual(visibleUIDs(), [], '列表应没有匹配的账号');
assert.deepStrictEqual(Array.from(ctx.groupBatchAccounts, s => s.uid), ['a1', 'a2']);

selectStatus('');
assert.strictEqual(groupScope.hidden, true);
ctx.accGroupFilter = '';
ctx.accRealmFilter = 'all';
ctx.render(rows);
assert.strictEqual(visibleUIDs().length, 4, '全部分组应显示全部 4 个');

// ── 2) 组内批量的谓词：不该动的账号一个都不许进目标集 ──
const G = ctx.actions;
assert.strictEqual(G.checkin.ok({}), true, '普通账号应参与组内签到');
assert.strictEqual(G.checkin.ok({ enterprise: true }), false, '企业版不该被组内签到');
assert.strictEqual(G.checkin.ok({ api_key: true }), false, '静态 Key 不该被组内签到');
assert.strictEqual(G.checkin.ok({ disabled: true }), false, '禁用号不该被组内签到');
assert.strictEqual(G.pause.ok({ paused: false }), true);
assert.strictEqual(G.pause.ok({ paused: true }), false, '已暂停的号不该再暂停一次');
assert.strictEqual(G.pause.ok({ disabled: true }), false, '禁用号不该走暂停');
assert.strictEqual(G.resume.ok({ paused: false }), false, '未暂停的号没有可恢复的');
assert.strictEqual(G.resume.ok({ paused: true }), true);
batch.then(() => {
  assert.deepStrictEqual(requests, [['POST', 'accounts/a1/pause']], '状态筛选不应改变组内批量的目标');
  process.stdout.write('OK');
}).catch(e => {
  console.error(e);
  process.exitCode = 1;
});
`
	dir := t.TempDir()
	f := filepath.Join(dir, "group-filter.cjs")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f, "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if string(out) != "OK" {
		t.Fatalf("unexpected output: %s", out)
	}
}
