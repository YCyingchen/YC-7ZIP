package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------ 假 QQ 开放平台

// fakeQQ 假装是 q.qq.com 的那两个接口。
//
// key 是本地在 create 时给过来的，密文得用同一把 key 加密才解得开——
// 这正是要验的那条链路，所以假服务器必须真的把它收下来。
type fakeQQ struct {
	server *httptest.Server

	mu      sync.Mutex
	creates []map[string]any
	polls   []map[string]any
	key     string
	taskID  string

	status   int
	appID    string
	secret   string
	openID   string
	retcode  int
	msg      string
	httpCode int
}

func newFakeQQ(t *testing.T) *fakeQQ {
	t.Helper()
	q := &fakeQQ{
		taskID:   "TASK-1",
		status:   1, // PENDING
		appID:    "102000001",
		secret:   "SECRET-abc",
		openID:   "OPENID-xyz",
		httpCode: http.StatusOK,
	}
	q.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		q.mu.Lock()
		httpCode, retcode, msg := q.httpCode, q.retcode, q.msg
		taskID, status, appID, secret, openID, key := q.taskID, q.status, q.appID, q.secret, q.openID, q.key
		switch r.URL.Path {
		case qqBindCreatePath:
			q.creates = append(q.creates, body)
			if k, _ := body["key"].(string); k != "" {
				q.key = k
				key = k
			}
		case qqBindPollPath:
			q.polls = append(q.polls, body)
		}
		q.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpCode)
		switch r.URL.Path {
		case qqBindCreatePath:
			_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": map[string]any{"task_id": taskID}})
		case qqBindPollPath:
			if retcode != 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"retcode": retcode, "msg": msg})
				return
			}
			data := map[string]any{"status": status, "bot_appid": appID, "user_openid": openID}
			if secret != "" && key != "" {
				if enc, err := sealSecret(key, secret); err == nil {
					data["bot_encrypt_secret"] = enc
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": data})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(q.server.Close)
	return q
}

func (q *fakeQQ) set(fn func(*fakeQQ)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	fn(q)
}

func (q *fakeQQ) pollCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.polls)
}

func (q *fakeQQ) keyB64() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.key
}

// sealSecret 按官方那套把 AppSecret 封起来：12 字节 IV + 密文 + 16 字节 tag。
func sealSecret(keyB64, secret string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	for i := range iv {
		iv[i] = byte(i)
	}
	out := append([]byte{}, iv...)
	out = append(out, gcm.Seal(nil, iv, []byte(secret), nil)...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// bindStore 造一个指向假 QQ 的 store。
func bindStore(t *testing.T, q *fakeQQ) *qqBindStore {
	t.Helper()
	b := newQQBindStore()
	b.host = q.server.URL
	b.client = q.server.Client()
	// 轮询节流是给真实界面用的（每两秒一次），用例里要连续问，所以放开；
	// 节流本身另有单独的用例。
	b.minInterval = 0
	return b
}

func testContext() context.Context { return context.Background() }

// onlySession 取出当前唯一的会话（用例里一次只开一个）。
func (b *qqBindStore) onlySession(t *testing.T) *qqBindSession {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sessions {
		return s
	}
	t.Fatal("没有会话")
	return nil
}

// ------------------------------------------------------------ 解密

func TestDecryptBindSecret(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	enc, err := sealSecret(key, "SECRET-value")
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptBindSecret(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SECRET-value" {
		t.Fatalf("解出来的 secret 不对：%q", got)
	}

	other := base64.StdEncoding.EncodeToString([]byte("ffffffffffffffffffffffffffffffff"))
	if _, err := decryptBindSecret(other, enc); err == nil {
		t.Fatal("用错 key 应该解不开")
	}
	if _, err := decryptBindSecret(key, ""); err == nil {
		t.Fatal("空密文应报错")
	}
	if _, err := decryptBindSecret(key, base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("过短的密文应报错")
	}
	if _, err := decryptBindSecret("不是 base64 的 key", enc); err == nil {
		t.Fatal("非法 key 应报错")
	}
}

// ------------------------------------------------------------ 全链路

func TestQQBindStoreRoundTrip(t *testing.T) {
	q := newFakeQQ(t)
	b := bindStore(t, q)

	sess, link, err := b.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" || sess.TaskID != "TASK-1" {
		t.Fatalf("会话不对：%+v", sess)
	}
	if !strings.Contains(link, "task_id=TASK-1") || !strings.Contains(link, "connect.html") {
		t.Fatalf("二维码链接不对：%s", link)
	}
	if !strings.Contains(link, "source=") {
		t.Fatalf("链接里应带来源标识：%s", link)
	}
	// 本地密钥不能出现在二维码里：那张图是要被扫、被截图、被转发的。
	if strings.Contains(link, sess.Key) {
		t.Fatal("本地密钥不该出现在二维码里")
	}

	got, err := b.poll(testContext(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != qqBindPending {
		t.Fatalf("还没扫时应是 pending，得到 %s", got.Status)
	}
	if got.AppSecret != "" {
		t.Fatal("没绑成功就不该有 secret")
	}
	if q.pollCount() != 1 {
		t.Fatalf("应转发过一次查询，得到 %d 次", q.pollCount())
	}

	q.set(func(q *fakeQQ) { q.status = 2 })
	got, err = b.poll(testContext(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != qqBindCompleted {
		t.Fatalf("应已完成，得到 %s（%s）", got.Status, got.Err)
	}
	if got.AppID != "102000001" || got.AppSecret != "SECRET-abc" || got.UserOpenID != "OPENID-xyz" {
		t.Fatalf("凭据不对：%+v", got)
	}
	// 完成之后状态是稳定的，不会退回去，也不再打扰官方。
	before := q.pollCount()
	again, _ := b.poll(testContext(), sess.ID)
	if again.Status != qqBindCompleted || again.AppSecret != "SECRET-abc" {
		t.Fatalf("已完成的状态应稳定：%+v", again)
	}
	if q.pollCount() != before {
		t.Fatal("已完成之后不该再问官方")
	}
}

func TestQQBindStorePollErrors(t *testing.T) {
	q := newFakeQQ(t)
	b := bindStore(t, q)

	if _, err := b.poll(testContext(), "不存在的会话"); err == nil {
		t.Fatal("未知会话应报错")
	}

	sess, _, err := b.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	// 抖动：状态保持 pending，只留一句提示——网络抖一下不该让人重新扫码。
	q.set(func(q *fakeQQ) { q.httpCode = http.StatusBadGateway })
	got, err := b.poll(testContext(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != qqBindPending || !strings.Contains(got.Err, "HTTP 502") {
		t.Fatalf("抖动应保持 pending 并说明原因：%+v", got)
	}
	q.set(func(q *fakeQQ) { q.httpCode = http.StatusOK; q.status = 2 })
	got, _ = b.poll(testContext(), sess.ID)
	if got.Status != qqBindCompleted {
		t.Fatalf("恢复后应能继续完成：%+v", got)
	}
}

func TestQQBindStoreProtocolFailures(t *testing.T) {
	// 官方报错（retcode != 0）→ failed，并带上原文
	q := newFakeQQ(t)
	b := bindStore(t, q)
	if _, _, err := b.start(testContext()); err != nil {
		t.Fatal(err)
	}
	sess := b.onlySession(t)
	q.set(func(q *fakeQQ) { q.retcode = 40054; q.msg = "task not found" })
	got, _ := b.poll(testContext(), sess.ID)
	if got.Status != qqBindFailed || !strings.Contains(got.Err, "task not found") {
		t.Fatalf("协议错应转 failed 并带上原因：%+v", got)
	}

	// 密文解不开（换了一把 key）→ failed
	q2 := newFakeQQ(t)
	b2 := bindStore(t, q2)
	sess2, _, err := b2.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	q2.set(func(q *fakeQQ) {
		q.status = 2
		q.key = base64.StdEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	})
	got2, _ := b2.poll(testContext(), sess2.ID)
	if got2.Status != qqBindFailed || !strings.Contains(got2.Err, "解不开") {
		t.Fatalf("解不开应转 failed：%+v", got2)
	}

	// 官方说过期（status 3）
	q3 := newFakeQQ(t)
	b3 := bindStore(t, q3)
	sess3, _, err := b3.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	q3.set(func(q *fakeQQ) { q.status = 3 })
	got3, _ := b3.poll(testContext(), sess3.ID)
	if got3.Status != qqBindExpired {
		t.Fatalf("过期应是 expired：%+v", got3)
	}
}

func TestQQBindStoreLocalExpiry(t *testing.T) {
	q := newFakeQQ(t)
	b := bindStore(t, q)
	sess, _, err := b.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	// 本地会话到点即过期：不依赖"官方什么时候说它过期了"。
	b.mu.Lock()
	b.sessions[sess.ID].ExpiresAt = time.Now().Add(-time.Second)
	b.mu.Unlock()
	got, err := b.poll(testContext(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != qqBindExpired {
		t.Fatalf("到点应过期：%+v", got)
	}
	if q.pollCount() != 0 {
		t.Fatal("本地已过期就不必再问官方")
	}
}

func TestQQBindStoreStartFailure(t *testing.T) {
	q := newFakeQQ(t)
	b := bindStore(t, q)
	q.set(func(q *fakeQQ) { q.httpCode = http.StatusInternalServerError })
	if _, _, err := b.start(testContext()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("HTTP 错误应报出来，得到 %v", err)
	}

	q.set(func(q *fakeQQ) { q.httpCode = http.StatusOK })
	if _, _, err := b.start(testContext()); err != nil {
		t.Fatalf("恢复后应能生成：%v", err)
	}
}

func TestQQBindStoreSweepAndCap(t *testing.T) {
	q := newFakeQQ(t)
	b := bindStore(t, q)

	for i := 0; i < qqBindMaxSessions+3; i++ {
		if _, _, err := b.start(testContext()); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	n := len(b.sessions)
	for _, s := range b.sessions { // 全部推成过期
		s.ExpiresAt = time.Now().Add(-time.Second)
	}
	b.mu.Unlock()
	if n > qqBindMaxSessions {
		t.Fatalf("会话数应被限制在 %d 以内，得到 %d", qqBindMaxSessions, n)
	}

	if _, _, err := b.start(testContext()); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	left := len(b.sessions)
	b.mu.Unlock()
	if left != 1 {
		t.Fatalf("过期会话应被清掉，只剩刚开的那个，得到 %d 个", left)
	}
}

func TestQQBindStoreThrottlesUpstreamPolls(t *testing.T) {
	q := newFakeQQ(t)
	b := newQQBindStore()
	b.host = q.server.URL
	b.client = q.server.Client()
	// 这里刻意保留默认节流：界面上是每两秒问一次，点上两下不该变成两次出站请求。

	sess, _, err := b.start(testContext())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := b.poll(testContext(), sess.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := q.pollCount(); got != 1 {
		t.Fatalf("连续三次轮询只该转发出一次，得到 %d 次", got)
	}
}

// ------------------------------------------------------------ 接口

func TestQQBindAPI(t *testing.T) {
	srv, ts := newHistoryTestServer(t)
	q := newFakeQQ(t)
	srv.qqBind = bindStore(t, q)

	code, raw := notifyCall(t, ts, http.MethodPost, "/api/notifications/qq/start", "")
	if code != http.StatusOK {
		t.Fatalf("start = %d：%s", code, raw)
	}
	var started struct {
		SessionID string `json:"session_id"`
		QRSVG     string `json:"qr_svg"`
		QRURL     string `json:"qr_url"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatal(err)
	}
	if started.SessionID == "" || !strings.HasPrefix(started.QRSVG, "<svg ") {
		t.Fatalf("start 应回会话号与二维码图片：%s", raw)
	}
	if !strings.Contains(started.QRURL, "task_id=") {
		t.Fatalf("应带上二维码链接，供复制链接兜底：%s", raw)
	}
	if started.ExpiresIn <= 0 || started.ExpiresIn > int(qqBindTTL.Seconds()) {
		t.Fatalf("有效期不对：%d", started.ExpiresIn)
	}
	if key := q.keyB64(); key != "" && strings.Contains(started.QRSVG, key) {
		t.Fatal("二维码里不该出现本地密钥")
	}

	code, raw = notifyCall(t, ts, http.MethodGet, "/api/notifications/qq/poll?session="+started.SessionID, "")
	if code != http.StatusOK {
		t.Fatalf("poll = %d：%s", code, raw)
	}
	var polled struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &polled); err != nil {
		t.Fatal(err)
	}
	if polled.Status != qqBindPending {
		t.Fatalf("刚生成应是 pending：%s", raw)
	}

	q.set(func(q *fakeQQ) { q.status = 2 })
	code, raw = notifyCall(t, ts, http.MethodGet, "/api/notifications/qq/poll?session="+started.SessionID, "")
	if code != http.StatusOK {
		t.Fatalf("poll = %d：%s", code, raw)
	}
	var done struct {
		Status    string `json:"status"`
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if err := json.Unmarshal(raw, &done); err != nil {
		t.Fatal(err)
	}
	if done.Status != qqBindCompleted || done.AppID != "102000001" || done.AppSecret != "SECRET-abc" {
		t.Fatalf("完成后应把凭据交给界面填表：%s", raw)
	}

	if code, _ := notifyCall(t, ts, http.MethodGet, "/api/notifications/qq/poll?session=nope", ""); code != http.StatusNotFound {
		t.Fatalf("未知会话应 404，得到 %d", code)
	}
}
