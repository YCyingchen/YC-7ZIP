package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/job"
)

// ------------------------------------------------------------ 假机器人

// fakeBot 是一个"记录收到了什么"的假机器人。
//
// 报文格式就是这些渠道唯一的契约，所以断言方法、路径、请求头与请求体，
// 比断言"没报错"有用得多。
type fakeBot struct {
	server *httptest.Server

	mu     sync.Mutex
	reqs   []botRequest
	status int
	body   string
}

type botRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func newFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	b := &fakeBot{status: http.StatusOK, body: `{"errcode":0,"errmsg":"ok"}`}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.reqs = append(b.reqs, botRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: raw})
		status, body := b.status, b.body
		b.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(b.server.Close)
	return b
}

func (b *fakeBot) reply(status int, body string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status, b.body = status, body
}

func (b *fakeBot) requests() []botRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]botRequest(nil), b.reqs...)
}

// jsonBody 解出第 i 个请求的 JSON 体。
func (b *fakeBot) jsonBody(t *testing.T, i int) map[string]any {
	t.Helper()
	reqs := b.requests()
	if i >= len(reqs) {
		t.Fatalf("只收到 %d 个请求，取不到第 %d 个", len(reqs), i)
	}
	var out map[string]any
	if err := json.Unmarshal(reqs[i].Body, &out); err != nil {
		t.Fatalf("请求体不是 JSON（%v）：%s", err, reqs[i].Body)
	}
	return out
}

// countTransport 拦下出站请求只做统计，用来验"什么时候不该发"。
type countTransport struct {
	mu   sync.Mutex
	urls []string
	fail error
}

func (c *countTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.urls = append(c.urls, r.URL.String())
	fail := c.fail
	c.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)),
		Request:    r,
	}, nil
}

func (c *countTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.urls)
}

func countTransportStore(t *testing.T, srv *Server, tr *countTransport) {
	t.Helper()
	srv.notifications.client = &http.Client{Transport: tr, Timeout: notifyTimeout}
}

func waitHits(t *testing.T, tr *countTransport, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tr.count() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到 %d 次发送，只有 %d 次", want, tr.count())
}

func testMessage() notifyMessage {
	return notifyMessage{
		Title: "YC-7ZIP 压缩完成",
		Lines: []string{"来源：/vol1/1000/a.txt", "用时：3.0 秒", "服务：http://127.0.0.1:8090"},
		Event: "compress.done", Service: "http://127.0.0.1:8090", Version: "test",
		Entry: HistoryEntry{
			ID: "j1", Kind: "compress", Status: "done",
			Sources: []string{"/vol1/1000/a.txt"}, OutputMode: "server",
			OutputDir: "/vol1/1000", Outputs: []HistoryFile{{Name: "a.7z", Size: 1024}},
			DurationMS: 3000, TotalBytes: 1024,
		},
	}
}

// ------------------------------------------------------------ 存储

func TestNotifyStoreDefaults(t *testing.T) {
	n := newNotifyStore(t.TempDir())
	got := n.snapshot()
	// 两个触发开关默认都关：升级上来的机器不该在用户没点头之前开始往外发消息。
	if got.Done || got.Error || len(got.Channels) != 0 {
		t.Fatalf("默认配置应为空且两个开关都关，得到 %+v", got)
	}
	if n.path != filepath.Join(filepath.Dir(n.path), notifyFileName) {
		t.Fatalf("配置文件名不对：%s", n.path)
	}
}

func TestNotifyStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	n := newNotifyStore(dir)
	if err := n.save(notifyFile{
		Enabled: notifyTriggers{Done: true},
		Channels: []NotifyChannel{
			{Type: notifyWeCom, URL: "https://example.com/hook", Enabled: true},
			{ID: "keep", Type: notifyQQBot, Name: "家里群", AppID: "102000001",
				AppSecret: "s3cret", TargetType: "group", TargetID: "OPENID1", Enabled: true},
			// 认不出的类型不该留在配置里：它永远发不出去，只会在列表里装样子。
			{Type: "telegram"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	again := newNotifyStore(dir)
	triggers, channels := again.active()
	if !triggers.Done || triggers.Error {
		t.Fatalf("触发开关没存住：%+v", triggers)
	}
	if len(channels) != 2 {
		t.Fatalf("应有 2 条渠道（未知类型被丢掉），得到 %d", len(channels))
	}
	if channels[0].ID == "" {
		t.Fatal("没有 id 的渠道应被补一个：整体替换时要靠它认出是同一条")
	}
	if channels[0].URL != "https://example.com/hook" || !channels[0].Enabled {
		t.Fatalf("地址或开关没存住：%+v", channels[0])
	}
	qq := channels[1]
	if qq.ID != "keep" || qq.AppID != "102000001" || qq.AppSecret != "s3cret" || qq.TargetID != "OPENID1" {
		t.Fatalf("QQ 渠道字段没存住：%+v", qq)
	}
	if qq.Name != "家里群" {
		t.Fatalf("名字没存住：%q", qq.Name)
	}

	// 文件里躺着 AppSecret，同机其他账号不该读得到。
	st, err := os.Stat(filepath.Join(dir, notifyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("配置里含 AppSecret，权限应为 0600，得到 %o", perm)
	}
}

func TestNotifyStoreBrokenFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, notifyFileName)
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	n := newNotifyStore(dir)
	got := n.snapshot()
	if got.Done || got.Error || len(got.Channels) != 0 {
		t.Fatalf("坏文件应退回默认值，得到 %+v", got)
	}
	// 而且坏了之后还能正常保存：不能因为读坏一次就永远写不进去。
	if err := n.save(notifyFile{Enabled: notifyTriggers{Error: true}}); err != nil {
		t.Fatal(err)
	}
	again := newNotifyStore(dir)
	triggers, _ := again.active()
	if !triggers.Error {
		t.Fatalf("坏文件之后保存应生效：%+v", triggers)
	}
}

func TestNotifySnapshotHidesSecrets(t *testing.T) {
	n := newNotifyStore(t.TempDir())
	if err := n.save(notifyFile{Channels: []NotifyChannel{
		{ID: "a", Type: notifyQQBot, AppID: "1", AppSecret: "topsecret", TargetID: "o", Enabled: true},
		{ID: "b", Type: notifyWebhook, URL: "https://example.com/h", HeaderName: "Authorization",
			HeaderValue: "Bearer tokenvalue", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(n.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "topsecret") || strings.Contains(string(raw), "Bearer tokenvalue") {
		t.Fatalf("密钥不该回给界面：%s", raw)
	}
	got := n.snapshot()
	if !got.Channels[0].HasSecret || !got.Channels[1].HasHeaderValue {
		t.Fatalf("应告诉界面密钥已存过：%+v", got.Channels)
	}

	// 界面把空值原样回传 = "这条密钥不动"，服务端按 id 补回。
	filled := n.withSecrets(NotifyChannel{ID: "a", Type: notifyQQBot})
	if filled.AppSecret != "topsecret" {
		t.Fatal("应按 id 把没回传的 AppSecret 补回去")
	}
	if h := n.withSecrets(NotifyChannel{ID: "b", Type: notifyWebhook}); h.HeaderValue != "Bearer tokenvalue" {
		t.Fatal("应按 id 把没回传的自定义头补回去")
	}
	// 没有 id 的当成新渠道，不能蹭到别人的密钥。
	if fresh := n.withSecrets(NotifyChannel{Type: notifyQQBot}); fresh.AppSecret != "" {
		t.Fatalf("新渠道不该带上别人的密钥：%q", fresh.AppSecret)
	}
	// 界面主动改了密钥时以界面为准。
	if changed := n.withSecrets(NotifyChannel{ID: "a", AppSecret: "new"}); changed.AppSecret != "new" {
		t.Fatalf("界面填的密钥应生效，得到 %q", changed.AppSecret)
	}
}

// ------------------------------------------------------------ 接口

func notifyCall(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func TestNotifyAPI(t *testing.T) {
	srv, ts := newHistoryTestServer(t)

	code, raw := notifyCall(t, ts, http.MethodGet, "/api/notifications", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/notifications = %d，想要 200", code)
	}
	var initial notifyPayload
	if err := json.Unmarshal(raw, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Done || initial.Error || len(initial.Channels) != 0 {
		t.Fatalf("默认应为空：%s", raw)
	}

	bot := newFakeBot(t)
	putBody := `{"done":true,"error":true,"channels":[
		{"type":"webhook","name":"钩子","url":"` + bot.server.URL + `/hook","enabled":true}]}`
	code, raw = notifyCall(t, ts, http.MethodPut, "/api/notifications", putBody)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/notifications = %d：%s", code, raw)
	}
	var saved notifyPayload
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Done || !saved.Error || len(saved.Channels) != 1 || saved.Channels[0].ID == "" {
		t.Fatalf("保存后应回一份带 id 的配置：%s", raw)
	}
	if saved.Channels[0].Name != "钩子" {
		t.Fatalf("名字没保存：%s", raw)
	}

	// 落盘了：新起一个 store（相当于重启服务）能读回来。
	reloaded := newNotifyStore(srv.cfg.DataDir)
	if triggers, channels := reloaded.active(); !triggers.Done || len(channels) != 1 {
		t.Fatalf("配置没落盘：%+v %+v", triggers, channels)
	}

	// 试发：走的是请求体里那条渠道，不要求先保存。
	code, raw = notifyCall(t, ts, http.MethodPost, "/api/notifications/test",
		`{"type":"webhook","url":"`+bot.server.URL+`/hook"}`)
	if code != http.StatusOK {
		t.Fatalf("POST /api/notifications/test = %d：%s", code, raw)
	}
	var result struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !strings.Contains(result.Detail, "HTTP 200") {
		t.Fatalf("试发应成功并带 HTTP 现场：%s", raw)
	}

	// 平台内部报错也要如实报出来（HTTP 200 + errcode 是这些平台的常态）。
	bot.reply(http.StatusOK, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
	code, raw = notifyCall(t, ts, http.MethodPost, "/api/notifications/test",
		`{"type":"wecom","url":"`+bot.server.URL+`/hook"}`)
	if code != http.StatusOK {
		t.Fatalf("试发失败也该是 200 + ok:false，得到 %d", code)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || !strings.Contains(result.Detail, "errcode=93000") {
		t.Fatalf("平台内部错误应报出来：%s", raw)
	}

	// 未知类型是请求写错了，直接 400。
	if code, _ := notifyCall(t, ts, http.MethodPost, "/api/notifications/test", `{"type":"telegram"}`); code != http.StatusBadRequest {
		t.Fatalf("未知渠道类型应 400，得到 %d", code)
	}
}

// TestNotifyPutKeepsStoredSecret 钉住一条很容易复发的坑：界面拿到的是脱敏配置，
// 它把这份配置整体回传时（点一下开关、改一下触发时机都是整体替换），
// 服务端必须按 id 把空着的密钥补回去，否则密钥会被"保存"这个动作悄悄抹掉。
func TestNotifyPutKeepsStoredSecret(t *testing.T) {
	srv, ts := newHistoryTestServer(t)

	put := func(body string) notifyPayload {
		t.Helper()
		code, raw := notifyCall(t, ts, http.MethodPut, "/api/notifications", body)
		if code != http.StatusOK {
			t.Fatalf("PUT = %d：%s", code, raw)
		}
		var out notifyPayload
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	saved := put(`{"done":true,"error":false,"channels":[{"id":"c1","type":"qqbot","name":"群",
		"app_id":"102000001","app_secret":"topsecret","target_type":"group","target_id":"G","enabled":true}]}`)
	if len(saved.Channels) != 1 || saved.Channels[0].AppSecret != "" || !saved.Channels[0].HasSecret {
		t.Fatalf("回读应脱敏：%+v", saved.Channels)
	}

	// 界面拿回读的那份原样回传，只把开关关掉。
	redacted := saved.Channels[0]
	body, err := json.Marshal(notifyPayload{Done: true, Channels: []NotifyChannel{redacted}})
	if err != nil {
		t.Fatal(err)
	}
	put(string(body))

	// 直接看落盘的那份：密钥必须还在。
	raw, err := os.ReadFile(filepath.Join(srv.cfg.DataDir, notifyFileName))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk notifyFile
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Channels) != 1 || onDisk.Channels[0].AppSecret != "topsecret" {
		t.Fatalf("整体替换不该把密钥抹掉：%s", raw)
	}

	// 而且它还能照常发出去（补回的密钥真的到了请求里）。
	bot := newFakeBot(t)
	bot.reply(http.StatusOK, `{"code":0,"message":""}`)
	tokenBot := newFakeBot(t)
	tokenBot.reply(http.StatusOK, `{"access_token":"AT","expires_in":"7200"}`)
	srv.notifications.tokenURL = tokenBot.server.URL
	srv.notifications.apiBase = bot.server.URL
	_, chans := srv.notifications.active()
	if len(chans) != 1 {
		t.Fatalf("应有一条启用的渠道，得到 %d", len(chans))
	}
	detail, ok := srv.notifications.send(context.Background(), chans[0], testMessage())
	if !ok {
		t.Fatalf("补回密钥后应能发出：%s", detail)
	}
	if got := bot.requests()[0].Header.Get("Authorization"); got != "QQBot AT" {
		t.Fatalf("Authorization 头不对：%q", got)
	}
}

// ------------------------------------------------------------ 各渠道的报文

func TestNotifyWeComBody(t *testing.T) {
	bot := newFakeBot(t)
	n := newNotifyStore(t.TempDir())

	detail, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyWeCom, URL: bot.server.URL + "/cgi-bin/webhook/send?key=K",
	}, testMessage())
	if !ok {
		t.Fatalf("应发送成功：%s", detail)
	}
	reqs := bot.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/cgi-bin/webhook/send" {
		t.Fatalf("请求不对：%+v", reqs)
	}
	if key := reqs[0].Header.Get("Content-Type"); !strings.HasPrefix(key, "application/json") {
		t.Fatalf("Content-Type 应为 json，得到 %q", key)
	}
	body := bot.jsonBody(t, 0)
	if body["msgtype"] != "markdown" {
		t.Fatalf("企业微信要走 markdown：%+v", body)
	}
	md, _ := body["markdown"].(map[string]any)
	content, _ := md["content"].(string)
	if !strings.HasPrefix(content, "### YC-7ZIP 压缩完成\n") || !strings.Contains(content, "来源：/vol1/1000/a.txt") {
		t.Fatalf("正文字不对：%q", content)
	}
}

func TestNotifyDingTalkBody(t *testing.T) {
	bot := newFakeBot(t)
	bot.reply(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`)
	n := newNotifyStore(t.TempDir())

	detail, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyDingTalk, URL: bot.server.URL + "/robot/send?access_token=T",
	}, testMessage())
	if !ok {
		t.Fatalf("应发送成功：%s", detail)
	}
	if got := bot.requests()[0].Path; got != "/robot/send" {
		t.Fatalf("路径不对：%s", got)
	}
	body := bot.jsonBody(t, 0)
	if body["msgtype"] != "markdown" {
		t.Fatalf("钉钉要走 markdown：%+v", body)
	}
	md, _ := body["markdown"].(map[string]any)
	if md["title"] != "YC-7ZIP 压缩完成" {
		t.Fatalf("标题应单独给（通知栏显示的就是它）：%+v", md)
	}
	text, _ := md["text"].(string)
	if !strings.Contains(text, "用时：3.0 秒") {
		t.Fatalf("正文不对：%q", text)
	}
}

func TestNotifyFeishuBody(t *testing.T) {
	bot := newFakeBot(t)
	bot.reply(http.StatusOK, `{"code":0,"msg":"success"}`)
	n := newNotifyStore(t.TempDir())

	detail, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyFeishu, URL: bot.server.URL + "/open-apis/bot/v2/hook/abc",
	}, testMessage())
	if !ok {
		t.Fatalf("应发送成功：%s", detail)
	}
	body := bot.jsonBody(t, 0)
	if body["msg_type"] != "text" {
		t.Fatalf("飞书走文本消息：%+v", body)
	}
	content, _ := body["content"].(map[string]any)
	text, _ := content["text"].(string)
	if !strings.HasPrefix(text, "YC-7ZIP 压缩完成\n") {
		t.Fatalf("正文应以标题开头：%q", text)
	}
}

func TestNotifyWebhookBody(t *testing.T) {
	bot := newFakeBot(t)
	bot.reply(http.StatusOK, `{"ok":true}`)
	n := newNotifyStore(t.TempDir())

	detail, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyWebhook, URL: bot.server.URL + "/hook",
		HeaderName: "Authorization", HeaderValue: "Bearer abc",
	}, testMessage())
	if !ok {
		t.Fatalf("应发送成功：%s", detail)
	}
	reqs := bot.requests()
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer abc" {
		t.Fatalf("自定义头没发出去：%q", got)
	}
	body := bot.jsonBody(t, 0)
	if body["source"] != "YC-7ZIP" || body["event"] != "compress.done" || body["title"] != "YC-7ZIP 压缩完成" {
		t.Fatalf("通用 webhook 的字段不齐：%+v", body)
	}
	if body["job"] != "j1" || body["status"] != "done" || body["kind"] != "compress" {
		t.Fatalf("任务字段不对：%+v", body)
	}
	if body["duration_ms"].(float64) != 3000 || body["total_bytes"].(float64) != 1024 {
		t.Fatalf("用时与大小应为数字：%+v", body)
	}
	if body["output_dir"] != "/vol1/1000" {
		t.Fatalf("输出目录不对：%+v", body)
	}
	outs, _ := body["outputs"].([]any)
	if len(outs) != 1 || outs[0] != "a.7z" {
		t.Fatalf("产物名单不对：%+v", body["outputs"])
	}
	if !strings.Contains(body["text"].(string), "服务：http://127.0.0.1:8090") {
		t.Fatalf("正文里应带上服务地址：%q", body["text"])
	}
}

func TestNotifyWebhookKeepsCustomHeaderOnly(t *testing.T) {
	bot := newFakeBot(t)
	bot.reply(http.StatusOK, `{}`)
	n := newNotifyStore(t.TempDir())

	// 只填头名不填值也是合法的（有些自建接口认一个固定值）。
	if _, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyWebhook, URL: bot.server.URL + "/hook", HeaderName: "X-Token",
	}, testMessage()); !ok {
		t.Fatal("应发送成功")
	}
	// 没填名字就不该凭空造一个头。
	bot.reply(http.StatusOK, `{}`)
	if _, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyWebhook, URL: bot.server.URL + "/hook",
	}, testMessage()); !ok {
		t.Fatal("应发送成功")
	}
}

func TestNotifyRejectsBadURL(t *testing.T) {
	n := newNotifyStore(t.TempDir())
	for _, raw := range []string{"", "file:///etc/passwd", "不是地址", "https://"} {
		detail, ok := n.send(context.Background(), NotifyChannel{Type: notifyWebhook, URL: raw}, testMessage())
		if ok {
			t.Fatalf("地址 %q 不该被放行", raw)
		}
		if detail == "" {
			t.Fatalf("地址 %q 失败时应给出原因", raw)
		}
	}
	// 不认识的类型也不能静默成功。
	if _, ok := n.send(context.Background(), NotifyChannel{Type: "telegram", URL: "https://example.com"}, testMessage()); ok {
		t.Fatal("不认识的类型不该报成功")
	}
}

// ------------------------------------------------------------ QQ 官方机器人

func TestNotifyQQBotUsesOfficialAPIAndCachesToken(t *testing.T) {
	tokenBot := newFakeBot(t)
	tokenBot.reply(http.StatusOK, `{"access_token":"AT-1","expires_in":"7200"}`)
	apiBot := newFakeBot(t)
	apiBot.reply(http.StatusOK, `{"code":0,"message":""}`)

	n := newNotifyStore(t.TempDir())
	n.tokenURL = tokenBot.server.URL
	n.apiBase = apiBot.server.URL

	ch := NotifyChannel{
		Type: notifyQQBot, AppID: "102000001", AppSecret: "sec",
		TargetType: "group", TargetID: "GROUPOPENID",
	}
	for i := 0; i < 2; i++ {
		detail, ok := n.send(context.Background(), ch, testMessage())
		if !ok {
			t.Fatalf("第 %d 次发送失败：%s", i+1, detail)
		}
	}

	// token 有效期 7200 秒，两次发送只该换一次——每条消息都去换会被频控拦。
	if got := len(tokenBot.requests()); got != 1 {
		t.Fatalf("两次发送只该取一次 token，得到 %d 次", got)
	}
	tokenReq := tokenBot.jsonBody(t, 0)
	if tokenReq["appId"] != "102000001" || tokenReq["clientSecret"] != "sec" {
		t.Fatalf("取 token 的请求体不对：%+v", tokenReq)
	}

	reqs := apiBot.requests()
	if len(reqs) != 2 {
		t.Fatalf("应有 2 条消息，得到 %d", len(reqs))
	}
	if reqs[0].Method != http.MethodPost || reqs[0].Path != "/v2/groups/GROUPOPENID/messages" {
		t.Fatalf("群消息的地址不对：%+v", reqs[0])
	}
	if got := reqs[0].Header.Get("Authorization"); got != "QQBot AT-1" {
		t.Fatalf("Authorization 头不对：%q", got)
	}
	msg := botJSON(t, reqs[0].Body)
	if msg["msg_type"].(float64) != 0 {
		t.Fatalf("msg_type 应为 0（纯文本）：%+v", msg)
	}
	if content, _ := msg["content"].(string); !strings.Contains(content, "YC-7ZIP 压缩完成") {
		t.Fatalf("消息内容不对：%q", content)
	}

	// 单聊是另一条路径。
	userCh := ch
	userCh.TargetType = "user"
	userCh.TargetID = "USEROPENID"
	if detail, ok := n.send(context.Background(), userCh, testMessage()); !ok {
		t.Fatalf("单聊发送失败：%s", detail)
	}
	if got := apiBot.requests()[2].Path; got != "/v2/users/USEROPENID/messages" {
		t.Fatalf("单聊的地址不对：%s", got)
	}
	if got := len(tokenBot.requests()); got != 1 {
		t.Fatalf("换目标类型不该重新取 token，得到 %d 次", got)
	}

	// 换了 AppSecret 就必须重取：拿着旧 token 发会 401。
	rotated := ch
	rotated.AppSecret = "sec2"
	tokenBot.reply(http.StatusOK, `{"access_token":"AT-2","expires_in":"7200"}`)
	if detail, ok := n.send(context.Background(), rotated, testMessage()); !ok {
		t.Fatalf("换密钥后发送失败：%s", detail)
	}
	if got := len(tokenBot.requests()); got != 2 {
		t.Fatalf("换密钥后应重新取 token，得到 %d 次", got)
	}
	if got := apiBot.requests()[3].Header.Get("Authorization"); got != "QQBot AT-2" {
		t.Fatalf("应改用新 token：%q", got)
	}
}

func TestNotifyQQBotTokenFailureIsReported(t *testing.T) {
	tokenBot := newFakeBot(t)
	tokenBot.reply(http.StatusUnauthorized, `{"code":100007,"message":"appid or secret wrong"}`)
	apiBot := newFakeBot(t)

	n := newNotifyStore(t.TempDir())
	n.tokenURL = tokenBot.server.URL
	n.apiBase = apiBot.server.URL

	detail, ok := n.send(context.Background(), NotifyChannel{
		Type: notifyQQBot, AppID: "1", AppSecret: "wrong", TargetType: "group", TargetID: "G",
	}, testMessage())
	if ok {
		t.Fatal("取不到 token 就不该报成功")
	}
	if !strings.Contains(detail, "access_token") || !strings.Contains(detail, "HTTP 401") {
		t.Fatalf("原因应说清是取 token 失败并带上现场：%q", detail)
	}
	if len(apiBot.requests()) != 0 {
		t.Fatal("没拿到 token 就不该去发消息")
	}

	// 缺字段也要说人话，而不是发一个注定失败的空请求。
	if detail, ok := n.send(context.Background(), NotifyChannel{Type: notifyQQBot, AppID: "1", AppSecret: "x"}, testMessage()); ok || !strings.Contains(detail, "openid") {
		t.Fatalf("缺 openid 应明确报出来：%q", detail)
	}
}

// botJSON 解一个请求体。
func botJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("请求体不是 JSON（%v）：%s", err, raw)
	}
	return out
}

// ------------------------------------------------------------ 触发与任务

func TestNotifyTriggersGateSending(t *testing.T) {
	srv, _ := newHistoryTestServer(t)
	tr := &countTransport{}
	countTransportStore(t, srv, tr)

	if err := srv.notifications.save(notifyFile{
		Enabled: notifyTriggers{Done: true},
		Channels: []NotifyChannel{
			{ID: "c1", Type: notifyWeCom, Name: "群", URL: "https://bot.invalid/hook", Enabled: true},
			{ID: "c2", Type: notifyWeCom, Name: "没开", URL: "https://bot.invalid/hook2"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 完成 → 发
	srv.notifyJob(HistoryEntry{ID: "j1", Kind: "compress", Status: "done"})
	waitHits(t, tr, 1)

	// 失败但开关没开 → 不发；取消无论开关如何都发（那是用户自己点掉的）
	srv.notifyJob(HistoryEntry{ID: "j2", Kind: "compress", Status: "error"})
	srv.notifyJob(HistoryEntry{ID: "j3", Kind: "compress", Status: "cancelled"})
	time.Sleep(150 * time.Millisecond)
	if got := tr.count(); got != 1 {
		t.Fatalf("开关没开的失败与取消都不该发，得到 %d 次", got)
	}

	// 打开失败开关 → 发
	triggers, channels := srv.notifications.active()
	if len(channels) != 1 {
		t.Fatalf("只有一条渠道开着，得到 %d", len(channels))
	}
	if err := srv.notifications.save(notifyFile{
		Enabled:  notifyTriggers{Done: triggers.Done, Error: true},
		Channels: channels,
	}); err != nil {
		t.Fatal(err)
	}
	srv.notifyJob(HistoryEntry{ID: "j4", Kind: "extract", Status: "error"})
	waitHits(t, tr, 2)

	// 全部关掉 → 不发
	if err := srv.notifications.save(notifyFile{
		Enabled:  notifyTriggers{},
		Channels: channels,
	}); err != nil {
		t.Fatal(err)
	}
	srv.notifyJob(HistoryEntry{ID: "j5", Kind: "compress", Status: "done"})
	time.Sleep(150 * time.Millisecond)
	if got := tr.count(); got != 2 {
		t.Fatalf("两个开关都关时不该发，得到 %d 次", got)
	}
}

// TestNotifyFailureDoesNotAffectJob 是这一块最要紧的一条：通知发不出去，
// 任务照旧成功收尾，历史照旧留下。
func TestNotifyFailureDoesNotAffectJob(t *testing.T) {
	srv, _ := newHistoryTestServer(t)
	tr := &countTransport{fail: errors.New("connection refused")}
	countTransportStore(t, srv, tr)

	if err := srv.notifications.save(notifyFile{
		Enabled: notifyTriggers{Done: true, Error: true},
		Channels: []NotifyChannel{
			{ID: "c1", Type: notifyWebhook, Name: "挂了", URL: "http://127.0.0.1:1/hook", Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	j, err := srv.jobs.Create(job.KindExtract)
	if err != nil {
		t.Fatal(err)
	}
	srv.launch(j, func(context.Context) error { return nil })

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, ok := srv.jobs.Get(j.ID)
		if ok && got.Status == job.StatusDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务没有成功收尾：%+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if entries := srv.history.list(); len(entries) != 1 {
		t.Fatalf("历史里应有 1 条，得到 %d", len(entries))
	}
	// 确实尝试发过（失败被吞进日志），而不是压根没走这条路。
	waitHits(t, tr, 1)
	if got, _ := srv.jobs.Get(j.ID); got.Status != job.StatusDone {
		t.Fatalf("通知失败不该改写任务状态：%s", got.Status)
	}
}

// TestNotifyIsAsync 钉住"通知不能拖住任务"：发送挂在网络上时，挂钩函数必须立刻返回。
func TestNotifyIsAsync(t *testing.T) {
	srv, _ := newHistoryTestServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv.notifications.client = &http.Client{
		Timeout:   notifyTimeout,
		Transport: blockingTransport{release: release},
	}

	if err := srv.notifications.save(notifyFile{
		Enabled: notifyTriggers{Done: true},
		Channels: []NotifyChannel{
			{ID: "c1", Type: notifyWebhook, URL: "http://10.255.255.1/hook", Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		srv.notifyJob(HistoryEntry{ID: "j1", Kind: "compress", Status: "done"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("notifyJob 不该等发送完成：发送必须异步")
	}
}

// blockingTransport 一直挂着，直到测试放行或请求被取消。
type blockingTransport struct{ release chan struct{} }

func (b blockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case <-b.release:
	case <-r.Context().Done():
	}
	return nil, errors.New("released")
}

// ------------------------------------------------------------ 消息内容

func TestNotifyMessageFromEntry(t *testing.T) {
	srv := &Server{cfg: Config{Addr: ":8090", BasePath: "/app/yc7zip", Version: "zip2609.027"}}
	msg := srv.buildNotifyMessage(HistoryEntry{
		ID: "j9", Kind: "extract", Status: "error",
		Sources:    []string{"/vol1/1000/a.7z", "b.7z", "c.7z", "d.7z"},
		OutputMode: "server", OutputDir: "/vol1/1000/out",
		Outputs:    []HistoryFile{{Name: "a.txt"}, {Name: "b.txt"}},
		TotalBytes: 2048, DurationMS: 3000,
		Message: "密码不正确",
	})

	if msg.Title != "YC-7ZIP 解压失败" {
		t.Fatalf("标题不对：%q", msg.Title)
	}
	if msg.Event != "extract.error" {
		t.Fatalf("事件名不对：%q", msg.Event)
	}
	body := msg.Body()
	for _, want := range []string{
		"来源：/vol1/1000/a.7z、b.7z、c.7z 等 4 项",
		"输出：/vol1/1000/out/a.txt 等 2 项",
		"用时：3.0 秒",
		"总大小：2.0 KB",
		"错误：密码不正确",
		"服务：http://127.0.0.1:8090/app/yc7zip",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("正文里缺 %q：\n%s", want, body)
		}
	}
}

func TestNotifyServiceURLFallbacks(t *testing.T) {
	// 飞牛包里只监听 unix socket，-addr 是空的：这时退回 BasePath，再退回项目名，
	// 而不是为此多一个必填配置项。
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Addr: ":8080"}, "http://127.0.0.1:8080"},
		{Config{Addr: "0.0.0.0:8090", BasePath: "/app/yc7zip/"}, "http://0.0.0.0:8090/app/yc7zip"},
		{Config{BasePath: "/app/yc7zip"}, "YC-7ZIP/app/yc7zip"},
		{Config{}, "YC-7ZIP"},
	}
	for _, c := range cases {
		srv := &Server{cfg: c.cfg}
		if got := srv.serviceURL(); got != c.want {
			t.Fatalf("serviceURL(%+v) = %q，想要 %q", c.cfg, got, c.want)
		}
	}
}

func TestNotifyTestMessageMarksItself(t *testing.T) {
	srv := &Server{cfg: Config{Addr: ":8080", Version: "test"}}
	msg := srv.testMessage()
	if !strings.Contains(msg.Title, "测试") {
		t.Fatalf("测试消息的标题应点明是测试：%q", msg.Title)
	}
	if msg.Event != "test" {
		t.Fatalf("事件名应为 test：%q", msg.Event)
	}
}
